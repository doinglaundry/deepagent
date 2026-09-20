package db

import (
	"errors"
	"gorm.io/gorm"
)

var (
	MySQLErrInvalidFilter       = errors.New("invalid MySQL filter")
	MySQLErrRecordNotFound      = gorm.ErrRecordNotFound
	MySQLErrTransactionRequired = errors.New("select for update requires a transaction")
)
