package common

type DbOperation int

const (
	OpInsert DbOperation = iota
	OpUpdate
	OpDelete
)

var DbOperationNames = map[DbOperation]string{
	OpInsert: "insert",
	OpUpdate: "update",
	OpDelete: "delete",
}
