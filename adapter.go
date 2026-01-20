package proto_gql_adapter

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/graphql-go/graphql"
)

// --------------------
// Caches (file-level) + locks
// --------------------
var (
	enumCache   = map[string]*graphql.Enum{}
	inputCache  = map[string]*graphql.InputObject{}
	outputCache = map[string]*graphql.Object{}

	cacheLock sync.RWMutex
)

// --------------------
// Public helpers
// --------------------

// ResetCaches clears all internally built types (useful before rebuilding schema)
func ResetCaches() {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	enumCache = map[string]*graphql.Enum{}
	inputCache = map[string]*graphql.InputObject{}
	outputCache = map[string]*graphql.Object{}
}

// ValidateCaches checks the caches for invalid/unnamed entries and returns an error if found.
// Call this before building the final schema to get a descriptive failure rather than a SIGSEGV.
func ValidateCaches() error {
	cacheLock.RLock()
	defer cacheLock.RUnlock()

	for k, v := range inputCache {
		if v == nil {
			return fmt.Errorf("inputCache contains nil for key: %s", k)
		}
		if strings.TrimSpace(v.Name()) == "" {
			return fmt.Errorf("inputCache contains unnamed InputObject for key: %s", k)
		}
	}

	for k, v := range outputCache {
		if v == nil {
			return fmt.Errorf("outputCache contains nil for key: %s", k)
		}
		if strings.TrimSpace(v.Name()) == "" {
			return fmt.Errorf("outputCache contains unnamed Object for key: %s", k)
		}
	}

	for k, v := range enumCache {
		if v == nil {
			return fmt.Errorf("enumCache contains nil for key: %s", k)
		}
		if strings.TrimSpace(v.Name()) == "" {
			return fmt.Errorf("enumCache contains unnamed Enum for key: %s", k)
		}
	}

	return nil
}

// --------------------
// Helpers
// --------------------
func makeTypeName(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "Anon"
	}
	return strings.ToUpper(base[:1]) + base[1:]
}

func normalizeEnumKey(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	l := strings.ToLower(v)
	// boolean-like mapping
	switch l {
	case "true", "yes", "y", "1":
		return "YES"
	case "false", "no", "n", "0":
		return "NO"
	}
	// general normalization
	key := strings.ToUpper(v)
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	if len(key) > 0 && key[0] >= '0' && key[0] <= '9' {
		key = "N_" + key
	}
	return key
}

// safe cache accessors ------------------------------------------------------

func getEnumFromCache(name string) (*graphql.Enum, bool) {
	cacheLock.RLock()
	defer cacheLock.RUnlock()
	e, ok := enumCache[name]
	if !ok || e == nil {
		return nil, false
	}
	return e, true
}

func setEnumInCache(name string, e *graphql.Enum) {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	enumCache[name] = e
}

func getInputFromCache(name string) (*graphql.InputObject, bool) {
	cacheLock.RLock()
	defer cacheLock.RUnlock()
	in, ok := inputCache[name]
	if !ok || in == nil {
		return nil, false
	}
	return in, true
}

func setInputInCache(name string, in *graphql.InputObject) {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	inputCache[name] = in
}

func getOutputFromCache(name string) (*graphql.Object, bool) {
	cacheLock.RLock()
	defer cacheLock.RUnlock()
	out, ok := outputCache[name]
	if !ok || out == nil {
		return nil, false
	}
	return out, true
}

func setOutputInCache(name string, out *graphql.Object) {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	outputCache[name] = out
}

// --------------------
// Enum creation (cached)
// --------------------
func CreateEnum(name string, field reflect.StructField) *graphql.Enum {
	if e, ok := getEnumFromCache(name); ok {
		return e
	}
	gqlTags := field.Tag.Get("gql")
	gqlItems := strings.Split(gqlTags, ";")
	enumFieldConfig := graphql.EnumConfig{
		Name:   name,
		Values: graphql.EnumValueConfigMap{},
	}
	for _, item := range gqlItems {
		item = strings.TrimSpace(item)
		if strings.HasPrefix(item, "enum=") {
			enumArray := strings.Split(strings.Split(item, "=")[1], ",")
			for enumIndex, enumItem := range enumArray {
				if enumIndex == 0 {
					continue
				}
				enumFieldConfig.Values[enumItem] = &graphql.EnumValueConfig{
					Value: enumIndex,
				}
			}
		}
	}
	enumObj := graphql.NewEnum(enumFieldConfig)
	setEnumInCache(name, enumObj)
	return enumObj
}
// --------------------
// Convert request struct -> graphql Args (uses internal caches)
// --------------------
func ConvertStructToGraphQLArgs(data interface{}) graphql.FieldConfigArgument {
	dataType := reflect.TypeOf(data)
	if dataType == nil {
		return graphql.FieldConfigArgument{}
	}
	if dataType.Kind() == reflect.Ptr {
		dataType = dataType.Elem()
	}
	fields := make(graphql.FieldConfigArgument)

	for i := 0; i < dataType.NumField(); i++ {
		field := dataType.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		gqlTags := field.Tag.Get("gql")
		isEnum := strings.Contains(gqlTags, "enum")
		isRequired := strings.Contains(gqlTags, "required")
		shouldIgnore := strings.Contains(gqlTags, "ignore")
		if shouldIgnore {
			continue
		}

		inputName := makeTypeName(jsonTag) + "Input"
		if isEnum {
			inputName = makeTypeName(jsonTag) + "EnumInput"
		}

		var inputType graphql.Input
		if in, ok := getInputFromCache(inputName); ok {
			inputType = in
		} else {
			if isEnum {
				enumName := makeTypeName(jsonTag) + "Enum"
				inputType = CreateEnum(enumName, field) // enum also valid as graphql.Input
			} else {
				inputType = getGraphQLInputType(field.Type, inputName)
			}
			// ensure not nil
			if inputType == nil {
				inputType = graphql.String
			}
			// if it is a fully-built named InputObject, ensure it's cached for reuse
			if obj, ok := inputType.(*graphql.InputObject); ok {
				setInputInCache(inputName, obj)
			}
		}

		// wrap slice once (avoid double wrapping)
		if field.Type.Kind() == reflect.Slice {
			if _, isList := inputType.(*graphql.List); !isList {
				inputType = graphql.NewList(inputType)
			}
		}

		if isRequired {
			inputType = graphql.NewNonNull(inputType)
		}

		fields[jsonTag] = &graphql.ArgumentConfig{Type: inputType}
	}

	return fields
}

// --------------------
// getGraphQLInputType (recursive, uses internal cache)
// --------------------
func getGraphQLInputType(t reflect.Type, typeName string) graphql.Input {
	if t == nil {
		return graphql.String
	}
	// pointers
	if t.Kind() == reflect.Ptr {
		if t.Elem().Kind() == reflect.Struct {
			return ConvertStructToGraphQLInput(t.Elem(), typeName)
		}
		return graphql.String
	}

	switch t.Kind() {
	case reflect.String:
		return graphql.String
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return graphql.Int
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return graphql.Int
	case reflect.Float32, reflect.Float64:
		return graphql.Float
	case reflect.Bool:
		return graphql.Boolean
	case reflect.Slice, reflect.Array:
		return getGraphQLInputType(t.Elem(), typeName)
		// if elem == nil {
		// 	return graphql.NewList(graphql.String)
		// }
		// return graphql.NewList(elem)
	case reflect.Struct:
		return ConvertStructToGraphQLInput(t, typeName)
	default:
		return graphql.String
	}
}

// --------------------
// Convert Struct -> GraphQL InputObject (with caching and named placeholder)
// --------------------
func ConvertStructToGraphQLInput(t reflect.Type, typeName string) *graphql.InputObject {
	if t == nil {
		// return a simple named empty input to avoid nil references
		return graphql.NewInputObject(graphql.InputObjectConfig{
			Name:   makeTypeName(typeName),
			Fields: graphql.InputObjectConfigFieldMap{},
		})
	}

	if typeName == "" {
		typeName = "AutoInput_" + t.Name()
	}
	typeName = makeTypeName(typeName)

	// fast path: cached
	if in, ok := getInputFromCache(typeName); ok {
		return in
	}

	// create a proper named placeholder (so GraphQL introspection has a Named type)
	placeholder := graphql.NewInputObject(graphql.InputObjectConfig{
		Name:   typeName,
		Fields: graphql.InputObjectConfigFieldMap{},
	})
	setInputInCache(typeName, placeholder)

	// compute fields
	fields := graphql.InputObjectConfigFieldMap{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		gqlTags := field.Tag.Get("gql")
		isEnum := strings.Contains(gqlTags, "enum")
		shouldIgnore := strings.Contains(gqlTags, "ignore")
		if shouldIgnore {
			continue
		}

		inputFieldName := makeTypeName(jsonTag) + "Input"
		if isEnum {
			inputFieldName = makeTypeName(jsonTag) + "EnumInput"
		}

		var inputType graphql.Input
		if in, ok := getInputFromCache(inputFieldName); ok {
			inputType = in
		} else {
			if isEnum {
				enumName := makeTypeName(jsonTag) + "Enum"
				inputType = CreateEnum(enumName, field)
			} else {
				inputType = getGraphQLInputType(field.Type, inputFieldName)
			}
			if inputType == nil {
				inputType = graphql.String
			}
		}

		// wrap slice once
		if field.Type.Kind() == reflect.Slice {
			if _, isList := inputType.(*graphql.List); !isList {
				inputType = graphql.NewList(inputType)
			}
		}

		if strings.Contains(gqlTags, "required") {
			inputType = graphql.NewNonNull(inputType)
		}

		fields[jsonTag] = &graphql.InputObjectFieldConfig{Type: inputType}
	}

	realObj := graphql.NewInputObject(graphql.InputObjectConfig{
		Name:   typeName,
		Fields: fields,
	})

	// replace placeholder with real object
	setInputInCache(typeName, realObj)
	return realObj
}

// --------------------
// Output types builders (recursive, uses internal cache)
// --------------------
func getGraphQLType(t reflect.Type, typeName string) graphql.Output {
	if t == nil {
		return graphql.String
	}
	// pointer
	if t.Kind() == reflect.Ptr {
		if t.Elem().Kind() == reflect.Struct {
			return ConvertStructToGraphQLObjectType(reflect.Zero(t.Elem()).Interface(), typeName)
		}
		return graphql.String
	}

	switch t.Kind() {
	case reflect.String:
		return graphql.String
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return graphql.Int
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return graphql.Int
	case reflect.Float32, reflect.Float64:
		return graphql.Float
	case reflect.Bool:
		return graphql.Boolean
	case reflect.Slice, reflect.Array:
		return getGraphQLType(t.Elem(), typeName)
		// if elem == nil {
		// 	return graphql.NewList(graphql.String)
		// }
		// return graphql.NewList(elem)
	case reflect.Struct:
		return ConvertStructToGraphQLObjectType(reflect.Zero(t).Interface(), typeName)
	default:
		return graphql.String
	}
}

// ConvertStructToGraphQLObjectType builds graphql.Object with caching and recursion safety
func ConvertStructToGraphQLObjectType(data interface{}, typeName string) *graphql.Object {
	// safety default for nil data
	if data == nil {
		return graphql.NewObject(graphql.ObjectConfig{
			Name:   makeTypeName(typeName),
			Fields: graphql.Fields{},
		})
	}

	if typeName == "" {
		typeName = "AutoType_" + reflect.TypeOf(data).Name()
	}
	typeName = makeTypeName(typeName)

	// fast cache check
	if out, ok := getOutputFromCache(typeName); ok {
		return out
	}

	// create named placeholder object
	shell := graphql.NewObject(graphql.ObjectConfig{
		Name:   typeName,
		Fields: graphql.Fields{},
	})
	setOutputInCache(typeName, shell)

	dataType := reflect.TypeOf(data)
	if dataType.Kind() == reflect.Ptr {
		dataType = dataType.Elem()
	}

	fields := graphql.Fields{}

	for i := 0; i < dataType.NumField(); i++ {
		field := dataType.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		gqlTags := field.Tag.Get("gql")
		isEnum := strings.Contains(gqlTags, "enum")

		base := makeTypeName(jsonTag)
		var refName string
		if isEnum {
			refName = base + "Enum"
		} else {
			refName = base + "Type"
		}

		var outType graphql.Output
		if isEnum {
			outType = CreateEnum(refName, field)
		} else {
			outType = getGraphQLType(field.Type, refName)
		}
		if outType == nil {
			outType = graphql.String
		}

		// if slice => list
		if field.Type.Kind() == reflect.Slice {
			outType = graphql.NewList(outType)
		}

		fields[jsonTag] = &graphql.Field{Type: outType}
	}

	// add fields to placeholder (AddFieldConfig is safe)
	for name, f := range fields {
		shell.AddFieldConfig(name, f)
	}

	return shell
}
