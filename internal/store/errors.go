package store

import (
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// IsDuplicate: lỗi trùng unique index.
func IsDuplicate(err error) bool { return mongo.IsDuplicateKeyError(err) }

func asBulkWriteException(err error, target *mongo.BulkWriteException) bool {
	return errors.As(err, target)
}
