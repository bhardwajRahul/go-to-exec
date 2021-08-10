package pkg

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"text/template"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type CompiledListener struct {
	config *ListenerConfig
	log    logrus.FieldLogger

	route string

	tplCmd   *template.Template
	tplArgs  []*template.Template
	tplEnv   map[string]*template.Template
	tplFiles map[string]*template.Template

	errorHandler *CompiledListener

	// Maps fixed file names to execution-time file names
	tplTmpFileNames map[string]interface{}
}

const funcMapKeyGTE = "gte"

func (listener *CompiledListener) clone() *CompiledListener {
	tplCmdClone, _ := listener.tplCmd.Clone()
	var tplArgsClones []*template.Template
	for _, tpl := range listener.tplArgs {
		clone, _ := tpl.Clone()
		tplArgsClones = append(tplArgsClones, clone)
	}
	tplEnvClones := make(map[string]*template.Template)
	for key, tpl := range listener.tplEnv {
		clone, _ := tpl.Clone()
		tplEnvClones[key] = clone
	}

	tplFilesClones := make(map[string]*template.Template)
	for key, tpl := range listener.tplFiles {
		clone, _ := tpl.Clone()
		tplFilesClones[key] = clone
	}

	newListener := &CompiledListener{
		listener.config,
		listener.log,
		listener.route,
		tplCmdClone,
		tplArgsClones,
		tplEnvClones,
		tplFilesClones,
		listener.errorHandler,
		// On clone, generate a new execution-time temporary files map
		map[string]interface{}{},
	}

	funcMap := template.FuncMap{
		funcMapKeyGTE: newListener.tplGTE,
	}

	// Replace the gte function in all cloned templates
	newListener.tplCmd.Funcs(funcMap)
	for _, tpl := range newListener.tplArgs {
		tpl.Funcs(funcMap)
	}
	for _, tpl := range newListener.tplEnv {
		tpl.Funcs(funcMap)
	}
	for _, tpl := range newListener.tplFiles {
		tpl.Funcs(funcMap)
	}

	return newListener
}

func (gte *GoToExec) compileListener(listenerConfig *ListenerConfig, route string, skipErrorHandler bool) *CompiledListener {
	log := logrus.WithField("listener", route)

	listenerConfig, err := mergeListenerConfig(&gte.config.Defaults, listenerConfig)
	if err != nil {
		log.WithError(err).Fatal("failed to merge listener config")
	}

	if err := validate.Struct(listenerConfig); err != nil {
		log.WithError(err).Fatal("failed to validate listener config")
	}

	listener := &CompiledListener{
		config: listenerConfig,
		log:    log,
		route:  route,
	}

	if !skipErrorHandler && listenerConfig.ErrorHandler != nil {
		listener.errorHandler = gte.compileListener(listenerConfig.ErrorHandler, fmt.Sprintf("%s-on-error", route), true)
	}

	tplFuncs := GetTPLFuncsMap()

	// Added here to make tpls parse, but will be overwritten on clone
	tplFuncs[funcMapKeyGTE] = listener.tplGTE

	// Creates a unique tmp directory where to store the files
	{
		tplFiles := make(map[string]*template.Template)
		for key, content := range listener.config.Files {
			filePath := key

			tpl, err := template.New(fmt.Sprintf("files-%s", key)).Funcs(tplFuncs).Parse(content)
			if err != nil {
				log.WithError(err).WithField("file", key).WithField("template", tpl).Fatal("failed to parse listener file template")
			}
			tplFiles[filePath] = tpl
		}
		listener.tplFiles = tplFiles
	}

	{
		tplCmd, err := template.New(route).Funcs(tplFuncs).Parse(listenerConfig.Command)
		if err != nil {
			log.WithError(err).WithField("template", listenerConfig.Command).Fatal("failed to parse listener command template")
		}
		listener.tplCmd = tplCmd
	}

	{
		var tplArgs []*template.Template
		for idx, str := range listenerConfig.Args {
			tpl, err := template.New(fmt.Sprintf("%s-%d", route, idx)).Funcs(tplFuncs).Parse(str)
			if err != nil {
				log.WithError(err).WithField("template", tpl).Fatal("failed to parse listener args template")
			}
			tplArgs = append(tplArgs, tpl)
		}
		listener.tplArgs = tplArgs
	}

	{
		tplEnv := make(map[string]*template.Template)
		for key, content := range listener.config.Env {
			tpl, err := template.New(fmt.Sprintf("env-%s", key)).Funcs(tplFuncs).Parse(content)
			if err != nil {
				log.WithError(err).WithField("file", key).WithField("template", tpl).Fatal("failed to parse listener env template")
			}
			tplEnv[key] = tpl
		}
		listener.tplEnv = tplEnv
	}

	return listener
}

func (listener *CompiledListener) tplGTE() map[string]interface{} {
	return map[string]interface{}{
		"files": listener.tplTmpFileNames,
	}
}

func (listener *CompiledListener) ExecCommand(args map[string]interface{}) (string, error) {
	/*
		Create a new instance of the listener, to handle temporary files.

		On every new run, we store files in different temporary folders, and we need to populate
		the "files" map of the template with different values, which means pointing the "gte" function
		to a different listener!
	*/
	l := listener.clone()

	log := l.log

	if boolVal(l.config.LogArgs) {
		log = log.WithField("args", args)
	}

	if listener.config.Trigger != nil {
		// The listener has a trigger condition, so evaluate it
		isTrue, err := listener.config.Trigger.IsTrue(args)
		if err != nil {
			err := errors.WithMessage(err, "failed to evaluate listener trigger condition")
			log.WithError(err).Error("error")
			return "", err
		}

		if !isTrue {
			// All good, do nothing
			return "not triggered", nil
		}
	}

	if err := l.processTemporaryFiles(args); err != nil {
		err := errors.WithMessage(err, "failed to process temporary files")
		log.WithError(err).Error("error")
		return "", err
	}
	defer l.cleanTemporaryFiles()

	var cmdStr string
	{
		out, err := ExecuteTemplate(l.tplCmd, args)
		if err != nil {
			err := errors.WithMessage(err, "failed to execute command template")
			log.WithError(err).Error("error")
			return "", err
		}
		cmdStr = out
	}

	var cmdArgs []string
	for _, tpl := range l.tplArgs {
		out, err := ExecuteTemplate(tpl, args)
		if err != nil {
			err := errors.WithMessagef(err, "failed to execute args template %s", tpl.Name())
			log.WithError(err).Error("error")
			return "", err
		}
		cmdArgs = append(cmdArgs, out)
	}

	var cmdEnv []string
	for key, tpl := range l.tplEnv {
		out, err := ExecuteTemplate(tpl, args)
		if err != nil {
			err := errors.WithMessagef(err, "failed to execute env template %s", tpl.Name())
			log.WithError(err).Error("error")
			return "", err
		}
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", key, out))
	}

	for cleanPath, realPath := range l.tplTmpFileNames {
		cmdEnv = append(cmdEnv, fmt.Sprintf("GTE_FILES_%s=%s", cleanPath, realPath))
	}

	if boolVal(l.config.LogCommand) {
		log = log.WithFields(logrus.Fields{
			"command":     cmdStr,
			"commandArgs": cmdArgs,
			"commandEnv":  cmdEnv,
		})
	}

	cmd := exec.Command(cmdStr, cmdArgs...)
	cmd.Env = os.Environ()

	for _, env := range cmdEnv {
		cmd.Env = append(cmd.Env, env)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := "failed to execute command"

		if boolVal(l.config.ReturnOutput) {
			msg += ": " + string(out)
		}

		err := errors.WithMessage(err, msg)

		log := log
		if boolVal(l.config.LogOutput) {
			log = log.WithField("output", string(out))
		}

		log.WithError(err).Error("error")

		if boolVal(l.config.ReturnOutput) {
			return string(out), err
		}

		return "", err
	}

	if boolVal(l.config.LogOutput) {
		log = log.WithField("output", string(out))
	}

	log.Info("command executed")

	if boolVal(l.config.ReturnOutput) {
		return string(out), nil
	}

	return "success", nil
}

var regexReplaceTemporaryFileName = regexp.MustCompile(`\W`)

// processTemporaryFiles stores temporary files defined in the "files" listener config entry in the right place
func (listener *CompiledListener) processTemporaryFiles(args map[string]interface{}) error {
	log := listener.log

	filesDir := ""
	tplTmpFileNames := make(map[string]interface{})
	for key, tpl := range listener.tplFiles {
		log := log.WithField("file", key)

		filePath := key
		if !path.IsAbs(filePath) {
			if filesDir == "" {
				_filesDir, err := os.MkdirTemp("", "gte-")
				if err != nil {
					err := errors.WithMessage(err, "failed to create temporary files directory")
					log.WithError(err).Error("error")
					return err
				}
				filesDir = _filesDir
			}
			filePath = filepath.Join(filesDir, filePath)
		}
		cleanFileName := regexReplaceTemporaryFileName.ReplaceAllString(key, "_")
		tplTmpFileNames[cleanFileName] = filePath

		out, err := ExecuteTemplate(tpl, args)
		if err != nil {
			err := errors.WithMessage(err, "failed to execute file template")
			log.WithError(err).Error("error")
			return err
		}

		if err := os.WriteFile(filePath, []byte(out), 0777); err != nil {
			err := errors.WithMessage(err, "failed to write file template")
			log.WithError(err).Error("error")
			return err
		}

		log.Debugf("written temporary file %s", filePath)
	}
	listener.tplTmpFileNames = tplTmpFileNames

	return nil
}

func (listener *CompiledListener) cleanTemporaryFiles() {
	log := listener.log

	for _, filePath := range listener.tplTmpFileNames {
		log := log.WithField("file", filePath)

		if err := os.Remove(filePath.(string)); err != nil {
			err := errors.WithMessage(err, "failed to remove file template")
			log.WithError(err).Error("error")
		}

		log.Debugf("removed temporary file %s", filePath)
	}
}
