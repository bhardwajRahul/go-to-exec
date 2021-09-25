package pkg

import (
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const keyAuthDefaultHTTPBasicUser = "gte"
const keyAuthApiKeyQuery = "__gteApiKey"
const keyArgsHeadersKey = "__gteHeaders"

func MountRoutes(engine *gin.Engine, config *Config) {
	storageCache := new(sync.Map)

	for route, listenerConfig := range config.Listeners {
		log := logrus.WithField("listener", route)

		listener := compileListener(&config.Defaults, listenerConfig, route, false, storageCache)
		handler := getGinListenerHandler(listener)

		if len(listener.config.Methods) == 0 {
			engine.GET(route, handler)
			engine.POST(route, handler)
		} else {
			for _, method := range listener.config.Methods {
				engine.Handle(method, route, handler)
			}
		}

		for _, plugin := range listener.plugins {
			if plugin, ok := plugin.(PluginHookMountRoutes); ok {
				plugin.HookMountRoutes(engine, listener.route, listener.HandleRequest)
			}
		}

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			log.WithFields(logrus.Fields{
				"config": spew.Sdump(listener.config),
			}).Debug("added listener")
		} else {
			log.Info("added listener")
		}
	}
}

// @formatter:off
/// [listener-response]
type ListenerResponse struct {
	*ExecCommandResult
	Storage            *StorageEntry     `json:"storage,omitempty"`
	Error              *string           `json:"error,omitempty"`
	ErrorHandlerResult *ListenerResponse `json:"errorHandlerResult,omitempty"`
}

/// [listener-response]
// @formatter:on

var regexListenerRouteCleaner = regexp.MustCompile(`[\W]`)

func getGinListenerHandler(listener *CompiledListener) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := verifyAuth(c, listener); err != nil {
			c.AbortWithError(http.StatusUnauthorized, err)
			return
		}

		args := make(map[string]interface{})

		// Use route params, if any
		for _, param := range c.Params {
			args[param.Key] = param.Value
		}

		// Add headers to args
		{
			headerMap := make(map[string]interface{})
			for k := range c.Request.Header {
				headerMap[strings.ToLower(k)] = c.GetHeader(k)
			}
			args[keyArgsHeadersKey] = headerMap
		}

		if c.Request.Method != http.MethodGet {
			b := binding.Default(c.Request.Method, c.ContentType())
			if b == binding.Form || b == binding.FormMultipart {
				queryMap := make(map[string][]string)
				if err := c.ShouldBindWith(&queryMap, b); err != nil {
					c.AbortWithError(http.StatusBadRequest, errors.WithMessage(err, "failed to parse request form body"))
					return
				}
				for key, vals := range queryMap {
					if len(vals) > 0 {
						args[key] = vals[len(vals)-1]
					} else {
						args[key] = true
					}

					args["_form_"+key] = vals
				}
			} else {
				if err := c.ShouldBindWith(&args, b); err != nil {
					c.AbortWithError(http.StatusBadRequest, errors.WithMessage(err, "failed to parse request body"))
					return
				}
			}
		}

		// Always bind query
		{
			queryMap := make(map[string][]string)
			if err := c.ShouldBindQuery(&queryMap); err != nil {
				c.AbortWithError(http.StatusBadRequest, errors.WithMessage(err, "failed to parse request query"))
				return
			}
			for key, vals := range queryMap {
				if len(vals) > 0 {
					args[key] = vals[len(vals)-1]
				} else {
					args[key] = true
				}

				args["_query_"+key] = vals
			}
		}

		ctxHandled, response, err := listener.HandleRequest(c, args)
		if ctxHandled {
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, response)
			return
		}

		c.JSON(http.StatusOK, response)
	}
}
