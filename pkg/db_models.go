package pkg

import (
	"gorm.io/gorm"
)

type DBModelRequest struct {
	gorm.Model

	// Request duration in millis
	Duration int64

	Storage *string
}
