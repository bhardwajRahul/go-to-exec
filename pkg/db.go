package pkg

import (
	"github.com/go-playground/validator/v10"
	"github.com/pkg/errors"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
)

// @formatter:off
/// [config-db-docs]
type DBConnSQLite struct {
	Conn string `mapstructure:"conn" validate:"required"`
}
type DBConnPostgres struct {
	Conn string `mapstructure:"conn" validate:"required"`
}
type DBConnMySQL struct {
	Conn string `mapstructure:"conn" validate:"required"`
}
type DBConnSQLServer struct {
	Conn string `mapstructure:"conn" validate:"required"`
}
type DBConnClickHouse struct {
	Conn string `mapstructure:"conn" validate:"required"`
}

type DBConfig struct {
	// Connection strings for all supported dbs
	// EXACTLY ONE needs to be used
	SQLite     *DBConnSQLite     `mapstructure:"sqlite"`
	Postgres   *DBConnPostgres   `mapstructure:"postgres"`
	MySQL      *DBConnMySQL      `mapstructure:"mysql"`
	SQLServer  *DBConnSQLServer  `mapstructure:"sqlserver"`
	ClickHouse *DBConnClickHouse `mapstructure:"clickhouse"`
}

/// [config-db-docs]
// @formatter:on

type DB struct {
	Conn *gorm.DB
}

func dbConfigValidator(sl validator.StructLevel) {
	if v, ok := sl.Current().Interface().(DBConfig); ok {
		if v.SQLite != nil || v.Postgres != nil || v.MySQL != nil || v.SQLServer != nil || v.ClickHouse != nil {
			return
		}
	}
	sl.ReportError(sl.Current().Interface(), "DBConfig", "", "", "")
}

func init() {
	Validate.RegisterStructValidation(dbConfigValidator, DBConfig{})
}

func NewDBConnection(config *DBConfig, log *GormLogger) (*DB, error) {
	var gormDB *gorm.DB
	var err error

	gormConfig := &gorm.Config{Logger: log}

	switch {
	case config.SQLite != nil:
		gormDB, err = gorm.Open(sqlite.Open(config.SQLite.Conn), gormConfig)
	case config.Postgres != nil:
		gormDB, err = gorm.Open(postgres.Open(config.Postgres.Conn), gormConfig)
	case config.MySQL != nil:
		gormDB, err = gorm.Open(mysql.Open(config.MySQL.Conn), gormConfig)
	case config.SQLServer != nil:
		gormDB, err = gorm.Open(sqlserver.Open(config.SQLServer.Conn), gormConfig)
	case config.ClickHouse != nil:
		gormDB, err = gorm.Open(clickhouse.Open(config.ClickHouse.Conn), gormConfig)
	}
	if err != nil {
		return nil, errors.WithMessage(err, "failed to open db")
	}

	db := &DB{gormDB}

	if err := db.initialize(); err != nil {
		return nil, errors.WithMessage(err, "failed to initialize db")
	}

	return db, nil
}

func (db *DB) initialize() error {
	models := []interface{}{
		// &Product{}
	}

	if err := db.Conn.AutoMigrate(models...); err != nil {
		return errors.WithMessage(err, "failed to auto migrate models")
	}

	return nil
}
