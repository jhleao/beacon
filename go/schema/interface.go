package schema

// ActionType enum
type IActionType string
type sActionType struct {
	Http IActionType
}

var ActionType = sActionType{
	Http: IActionType("http"),
}

var ActionTypes = []IActionType{
	ActionType.Http,
}

// HttpMethod enum
type IHttpMethod string
type sHttpMethod struct {
	Get    IHttpMethod
	Post   IHttpMethod
	Put    IHttpMethod
	Patch  IHttpMethod
	Delete IHttpMethod
}

var HttpMethod = sHttpMethod{
	Get:    IHttpMethod("get"),
	Post:   IHttpMethod("post"),
	Put:    IHttpMethod("put"),
	Patch:  IHttpMethod("patch"),
	Delete: IHttpMethod("delete"),
}

var HttpMethods = []IHttpMethod{
	HttpMethod.Get,
	HttpMethod.Post,
	HttpMethod.Put,
	HttpMethod.Patch,
	HttpMethod.Delete,
}

// Data operation enum
type IOperation string
type sOperation struct {
	Insert IOperation
	Update IOperation
	Delete IOperation
}

var Operation = sOperation{
	Insert: IOperation("insert"),
	Update: IOperation("update"),
	Delete: IOperation("delete"),
}

var Operations = []IOperation{
	Operation.Insert,
	Operation.Update,
	Operation.Delete,
}

type Schema struct {
	Version     int          `yaml:"version"`
	Driver      string       `yaml:"driver"`
	Definitions []Definition `yaml:"definitions"`
}

// Entries defined in the schema
type Definition struct {
	Trigger Trigger `yaml:"trigger"`
	Action  Action  `yaml:"action"`
}

// Data necessary to uniquely identify a database trigger
type Trigger struct {
	Schema    string     `yaml:"schema" json:"schema"`
	Table     string     `yaml:"table" json:"table"`
	Operation IOperation `yaml:"operation" json:"operation"`
}

// Configuration for actions performed as result of events
type Action struct {
	// TODO: How would we represent unions for other trigger types?
	Type   IActionType `yaml:"type" json:"type"`
	Method IHttpMethod `yaml:"method" json:"method"`
	Url    string      `yaml:"url" json:"url"`
}

// Data sent over to external consumers
type NotificationBody struct {
	Trigger Trigger     `json:"trigger"`
	Old     interface{} `json:"old"`
	New     interface{} `json:"new"`
}

type Parser interface {
	ValidateAndParse(rawSchema []byte) (Schema, error)
}
