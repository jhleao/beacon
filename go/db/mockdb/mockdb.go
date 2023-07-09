package mockdb

import (
	"beacon/go/db"
	"beacon/go/schema"

	"github.com/stretchr/testify/mock"
)

type MockConnector struct {
	mock.Mock
}

func (m *MockConnector) Initialize() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockConnector) Subscribe(cb func(p db.EventPayload)) int {
	args := m.Called(cb)
	return args.Int(0)
}

func (m *MockConnector) ApplySchema(s schema.Schema) error {
	args := m.Called(s)
	return args.Error(0)
}

func (m *MockConnector) GetTableNamesOnSchema(sch string) ([]string, error) {
	args := m.Called(sch)
	return args.Get(0).([]string), args.Error(1)
}
