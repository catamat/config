package config

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/catamat/secure"
)

type valueSource string

const (
	sourceEnv  valueSource = "env"
	sourceFlag valueSource = "flag"
)

const (
	jsonSecretTag    = "secret"
	jsonSecretPrefix = "enc:v1:"
)

var textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

type fieldAccessor struct {
	root  reflect.Value
	index []int
}

type flagFieldValue struct {
	accessor  fieldAccessor
	fieldType reflect.Type
	valueSep  string
	seen      bool
}

// JSONOption configures the behavior of FromJSON.
type JSONOption func(*jsonOptions) error

type jsonOptions struct {
	secretPassphrase []byte
}

// WithJSONSecrets enables transparent encryption for JSON fields tagged with `secret:"true"`.
//
// Tagged string values are returned in plaintext in memory. If the file still contains plaintext,
// it is automatically rewritten using AES-GCM with the passphrase-derived key.
func WithJSONSecrets(passphrase []byte) JSONOption {
	return func(options *jsonOptions) error {
		if len(passphrase) == 0 {
			return errors.New("config: JSON secret passphrase cannot be empty")
		}

		options.secretPassphrase = append([]byte(nil), passphrase...)
		return nil
	}
}

// FromJSON loads the JSON configuration file from the path and tries to convert it into a structure.
func FromJSON(structure interface{}, path string, options ...JSONOption) error {
	if err := validatePointerTarget(structure); err != nil {
		return err
	}

	jsonOptions, err := buildJSONOptions(options)
	if err != nil {
		return err
	}

	file, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if raw, ok := structure.(*[]byte); ok {
		*raw = file
		return nil
	}

	if err := json.Unmarshal(file, structure); err != nil {
		return err
	}

	if len(jsonOptions.secretPassphrase) == 0 {
		return nil
	}

	structElem, err := validateStructTarget(structure)
	if err != nil {
		return err
	}

	updated, err := revealJSONSecrets(structElem, jsonOptions.secretPassphrase, "")
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}

	data, err := marshalEncryptedJSON(structure, jsonOptions.secretPassphrase)
	if err != nil {
		return err
	}

	return os.WriteFile(path, normalizeJSONLineEndings(data, file), 0o600)
}

// FromEnv loads the configuration vars from the environment and tries to convert them into a structure.
func FromEnv(structure interface{}) error {
	structElem, err := validateStructTarget(structure)
	if err != nil {
		return err
	}

	for _, s := range os.Environ() {
		key, value, ok := splitKeyValue(s)
		if !ok {
			continue
		}

		if err := populateStruct(structElem, key, value, sourceEnv); err != nil {
			return err
		}
	}

	return nil
}

// FromFlags loads the configuration flags from the command-line and tries to convert them into a structure.
func FromFlags(structure interface{}) error {
	structElem, err := validateStructTarget(structure)
	if err != nil {
		return err
	}

	flagSet := flag.NewFlagSet(commandName(), flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	if err := registerFlags(flagSet, structElem); err != nil {
		return err
	}

	return flagSet.Parse(os.Args[1:])
}

func validatePointerTarget(target interface{}) error {
	if target == nil {
		return errors.New("config: target cannot be nil")
	}

	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("config: target must be a non-nil pointer")
	}

	return nil
}

func validateStructTarget(target interface{}) (reflect.Value, error) {
	if err := validatePointerTarget(target); err != nil {
		return reflect.Value{}, err
	}

	value := reflect.ValueOf(target).Elem()
	if value.Kind() != reflect.Struct {
		return reflect.Value{}, errors.New("config: target must point to a struct")
	}

	return value, nil
}

func splitKeyValue(input string) (string, string, bool) {
	if !strings.Contains(input, "=") {
		return "", "", false
	}

	parts := strings.SplitN(input, "=", 2)
	key, value := parts[0], parts[1]
	if key == "" {
		return "", "", false
	}

	return key, value, true
}

func commandName() string {
	if len(os.Args) == 0 {
		return ""
	}

	return os.Args[0]
}

func registerFlags(flagSet *flag.FlagSet, structElem reflect.Value) error {
	seen := make(map[string]string)
	return registerStructFlags(flagSet, structElem, structElem, "", nil, seen)
}

func registerStructFlags(flagSet *flag.FlagSet, root reflect.Value, structElem reflect.Value, path string, indexPath []int, seen map[string]string) error {
	for i := 0; i < structElem.NumField(); i++ {
		fieldType := structElem.Type().Field(i)
		if !fieldType.IsExported() {
			continue
		}
		if tagValue, ok := fieldType.Tag.Lookup(string(sourceFlag)); ok && tagValue == "-" {
			continue
		}

		field := structElem.Field(i)
		fieldPath := joinFieldPath(path, fieldType.Name)
		fieldIndexPath := appendIndex(indexPath, i)

		if supportsFlagField(fieldType.Type) {
			flagName, err := normalizedFlagName(fieldType)
			if err != nil {
				return fmt.Errorf("config: field %s: %w", fieldPath, err)
			}
			if previousPath, ok := seen[flagName]; ok {
				return fmt.Errorf("config: duplicate flag %q for fields %s and %s", flagName, previousPath, fieldPath)
			}

			seen[flagName] = fieldPath
			flagSet.Var(&flagFieldValue{
				accessor: fieldAccessor{
					root:  root,
					index: fieldIndexPath,
				},
				fieldType: fieldType.Type,
				valueSep:  fieldValueSeparator(fieldType),
			}, flagName, "")
			continue
		}

		nestedStruct, ok := nestedStructForRegistration(field)
		if !ok {
			if tagValue, ok := fieldType.Tag.Lookup(string(sourceFlag)); ok && tagValue != "" {
				return fmt.Errorf("config: field %s: unsupported flag type %s", fieldPath, fieldType.Type)
			}

			continue
		}

		if err := registerStructFlags(flagSet, root, nestedStruct, fieldPath, fieldIndexPath, seen); err != nil {
			return err
		}
	}

	return nil
}

func supportsFlagField(fieldType reflect.Type) bool {
	if typeSupportsTextUnmarshaler(fieldType) {
		return true
	}

	switch fieldType.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Bool,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return supportsFlagField(fieldType.Elem())
	case reflect.Pointer:
		if fieldType.Implements(textUnmarshalerType) {
			return true
		}

		return fieldType.Elem().Kind() != reflect.Struct && supportsFlagField(fieldType.Elem())
	default:
		return false
	}
}

func typeSupportsTextUnmarshaler(fieldType reflect.Type) bool {
	if fieldType.Implements(textUnmarshalerType) {
		return true
	}

	if fieldType.Kind() != reflect.Pointer {
		return reflect.PointerTo(fieldType).Implements(textUnmarshalerType)
	}

	return false
}

func normalizedFlagName(fieldType reflect.StructField) (string, error) {
	fieldName, ok := sourceFieldKey(fieldType, sourceFlag)
	if !ok {
		return "", errors.New("flag field is disabled")
	}

	flagName := strings.TrimLeft(fieldName, "-")
	if flagName == "" {
		return "", errors.New("flag name cannot be empty")
	}

	return flagName, nil
}

func fieldValueSeparator(fieldType reflect.StructField) string {
	if fieldType.Tag.Get("vsep") != "" {
		return fieldType.Tag.Get("vsep")
	}

	return ":"
}

func nestedStructForRegistration(field reflect.Value) (reflect.Value, bool) {
	if supportsTextUnmarshaler(field) {
		return reflect.Value{}, false
	}

	switch field.Kind() {
	case reflect.Struct:
		return field, true
	case reflect.Pointer:
		if field.Type().Elem().Kind() != reflect.Struct || field.Type().Implements(textUnmarshalerType) {
			return reflect.Value{}, false
		}
		if field.IsNil() {
			return reflect.New(field.Type().Elem()).Elem(), true
		}

		return field.Elem(), true
	default:
		return reflect.Value{}, false
	}
}

func appendIndex(indexPath []int, index int) []int {
	result := make([]int, len(indexPath)+1)
	copy(result, indexPath)
	result[len(indexPath)] = index
	return result
}

func (a fieldAccessor) field(create bool) (reflect.Value, bool, error) {
	if len(a.index) == 0 {
		return reflect.Value{}, false, errors.New("config: invalid field path")
	}

	current := a.root
	for i, index := range a.index {
		field := current.Field(index)
		if i == len(a.index)-1 {
			return field, true, nil
		}

		switch field.Kind() {
		case reflect.Struct:
			current = field
		case reflect.Pointer:
			if field.Type().Elem().Kind() != reflect.Struct {
				return reflect.Value{}, false, errors.New("config: invalid nested pointer field")
			}
			if field.IsNil() {
				if !create {
					return reflect.Value{}, false, nil
				}

				field.Set(reflect.New(field.Type().Elem()))
			}

			current = field.Elem()
		default:
			return reflect.Value{}, false, errors.New("config: invalid nested field path")
		}
	}

	return reflect.Value{}, false, errors.New("config: invalid field path")
}

func (v *flagFieldValue) Set(value string) error {
	field, ok, err := v.accessor.field(true)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("config: field is not available")
	}

	return setFlagField(field, value, v.valueSep, &v.seen)
}

func (v *flagFieldValue) String() string {
	field, ok, err := v.accessor.field(false)
	if err != nil || !ok {
		return ""
	}

	return formatFlagFieldValue(field, v.valueSep)
}

func (v *flagFieldValue) IsBoolFlag() bool {
	return isBoolFlagType(v.fieldType)
}

func setFlagField(field reflect.Value, fieldValue string, valueSep string, seen *bool) error {
	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}

		if field.Type().Implements(textUnmarshalerType) {
			*seen = true
			return populateField(field, fieldValue)
		}

		return setFlagField(field.Elem(), fieldValue, valueSep, seen)
	}

	if field.Kind() == reflect.Slice {
		if !*seen {
			field.Set(reflect.MakeSlice(field.Type(), 0, 0))
		}

		*seen = true
		return populateSlice(field, fieldValue, valueSep)
	}

	*seen = true
	return populateField(field, fieldValue)
}

func formatFlagFieldValue(field reflect.Value, valueSep string) string {
	if !field.IsValid() {
		return ""
	}

	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return ""
		}

		return formatFlagFieldValue(field.Elem(), valueSep)
	}

	if field.Kind() == reflect.Slice {
		if field.Len() == 0 {
			return ""
		}

		parts := make([]string, field.Len())
		for i := 0; i < field.Len(); i++ {
			parts[i] = formatFlagFieldValue(field.Index(i), valueSep)
		}

		return strings.Join(parts, valueSep)
	}

	return fmt.Sprint(field.Interface())
}

func isBoolFlagType(fieldType reflect.Type) bool {
	for fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
	}

	return fieldType.Kind() == reflect.Bool
}

// populateStruct tries to populate the input structure using the key and value strings.
func populateStruct(structElem reflect.Value, dataKey string, dataValue string, source valueSource) error {
	_, err := populateStructFields(structElem, dataKey, dataValue, source, "")
	return err
}

func populateStructFields(structElem reflect.Value, dataKey string, dataValue string, source valueSource, path string) (bool, error) {
	var matched bool

	for i := 0; i < structElem.NumField(); i++ {
		fieldType := structElem.Type().Field(i)
		if !fieldType.IsExported() {
			continue
		}

		field := structElem.Field(i)
		fieldPath := joinFieldPath(path, fieldType.Name)
		fieldKey, ok := sourceFieldKey(fieldType, source)

		if ok && fieldKey == dataKey {
			if matched {
				return false, fmt.Errorf("config: multiple fields match %q", dataKey)
			}

			if err := populateValue(field, fieldType, dataValue); err != nil {
				return false, fmt.Errorf("config: field %s: %w", fieldPath, err)
			}

			matched = true
		}

		nestedMatched, err := populateNestedStructField(field, dataKey, dataValue, source, fieldPath)
		if err != nil {
			return false, err
		}
		if nestedMatched {
			if matched {
				return false, fmt.Errorf("config: multiple fields match %q", dataKey)
			}

			matched = true
		}
	}

	return matched, nil
}

func sourceFieldKey(fieldType reflect.StructField, source valueSource) (string, bool) {
	if tagValue, ok := fieldType.Tag.Lookup(string(source)); ok {
		if tagValue == "-" {
			return "", false
		}

		return tagValue, true
	}

	return fieldType.Name, true
}

func joinFieldPath(path string, fieldName string) string {
	if path == "" {
		return fieldName
	}

	return path + "." + fieldName
}

func populateNestedStructField(field reflect.Value, dataKey string, dataValue string, source valueSource, path string) (bool, error) {
	if supportsTextUnmarshaler(field) {
		return false, nil
	}

	switch field.Kind() {
	case reflect.Struct:
		return populateStructFields(field, dataKey, dataValue, source, path)
	case reflect.Pointer:
		if field.Type().Elem().Kind() != reflect.Struct || field.Type().Implements(textUnmarshalerType) {
			return false, nil
		}

		if field.IsNil() {
			nested := reflect.New(field.Type().Elem()).Elem()
			matched, err := populateStructFields(nested, dataKey, dataValue, source, path)
			if err != nil {
				return false, err
			}
			if matched {
				ptr := reflect.New(field.Type().Elem())
				ptr.Elem().Set(nested)
				field.Set(ptr)
			}

			return matched, nil
		}

		return populateStructFields(field.Elem(), dataKey, dataValue, source, path)
	default:
		return false, nil
	}
}

func supportsTextUnmarshaler(field reflect.Value) bool {
	if !field.IsValid() {
		return false
	}

	if field.Type().Implements(textUnmarshalerType) {
		return true
	}

	return field.CanAddr() && field.Addr().Type().Implements(textUnmarshalerType)
}

func populateValue(field reflect.Value, fieldType reflect.StructField, fieldValue string) error {
	switch field.Kind() {
	case reflect.Struct:
		if supportsTextUnmarshaler(field) {
			return populateField(field, fieldValue)
		}

		return errors.New("struct values are not directly supported")
	case reflect.Pointer:
		if field.Type().Implements(textUnmarshalerType) {
			if field.IsNil() {
				field.Set(reflect.New(field.Type().Elem()))
			}

			return populateField(field, fieldValue)
		}

		if field.Type().Elem().Kind() == reflect.Struct {
			return errors.New("struct values are not directly supported")
		}

		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}

		return populateValue(field.Elem(), fieldType, fieldValue)
	case reflect.Slice:
		valueSep := ":"
		if fieldType.Tag.Get("vsep") != "" {
			valueSep = fieldType.Tag.Get("vsep")
		}

		return populateSlice(field, fieldValue, valueSep)
	default:
		return populateField(field, fieldValue)
	}
}

// populateSlice splits sliceValue by valueSep and tries to populate the input slice.
func populateSlice(slice reflect.Value, sliceValue string, valueSep string) error {
	if !slice.IsValid() {
		return errors.New("field is not valid")
	}
	if !slice.CanSet() {
		return errors.New("field cannot be set")
	}

	if sliceValue == "" {
		slice.Set(reflect.MakeSlice(slice.Type(), 0, 0))
		return nil
	}

	sliceType := slice.Type().Elem()
	parts := strings.Split(sliceValue, valueSep)
	newSlice := reflect.MakeSlice(slice.Type(), 0, len(parts))

	for _, part := range parts {
		field := reflect.New(sliceType).Elem()
		if err := populateField(field, part); err != nil {
			return err
		}

		newSlice = reflect.Append(newSlice, field)
	}

	slice.Set(newSlice)
	return nil
}

// populateField tries to populate the input field.
func populateField(field reflect.Value, fieldValue string) error {

	if !field.IsValid() {
		return errors.New("field is not valid")
	}
	if !field.CanSet() {
		return errors.New("field cannot be set")
	}

	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}

		if field.Type().Implements(textUnmarshalerType) {
			return field.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(fieldValue))
		}

		return populateField(field.Elem(), fieldValue)
	}

	if field.CanAddr() && field.Addr().Type().Implements(textUnmarshalerType) {
		return field.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(fieldValue))
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(fieldValue)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bitSize := 0
		switch field.Kind() {
		case reflect.Int8:
			bitSize = 8
		case reflect.Int16:
			bitSize = 16
		case reflect.Int32:
			bitSize = 32
		case reflect.Int64:
			bitSize = 64
		}
		r, err := strconv.ParseInt(fieldValue, 10, bitSize)
		if err != nil {
			return fmt.Errorf("failed to parse %s as int: %v", fieldValue, err)
		}
		field.SetInt(r)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		bitSize := 0
		switch field.Kind() {
		case reflect.Uint8:
			bitSize = 8
		case reflect.Uint16:
			bitSize = 16
		case reflect.Uint32:
			bitSize = 32
		case reflect.Uint64:
			bitSize = 64
		case reflect.Uintptr:
			bitSize = strconv.IntSize
		}
		r, err := strconv.ParseUint(fieldValue, 10, bitSize)
		if err != nil {
			return fmt.Errorf("failed to parse %s as uint: %v", fieldValue, err)
		}
		field.SetUint(r)
	case reflect.Bool:
		r, err := strconv.ParseBool(fieldValue)
		if err != nil {
			return fmt.Errorf("failed to parse %s as bool: %v", fieldValue, err)
		}
		field.SetBool(r)
	case reflect.Float32, reflect.Float64:
		bitSize := 32
		if field.Kind() == reflect.Float64 {
			bitSize = 64
		}
		r, err := strconv.ParseFloat(fieldValue, bitSize)
		if err != nil {
			return fmt.Errorf("failed to parse %s as float: %v", fieldValue, err)
		}
		field.SetFloat(r)
	default:
		return fmt.Errorf("cannot handle field kind: %v", field.Type().Kind())
	}

	return nil
}

func buildJSONOptions(options []JSONOption) (jsonOptions, error) {
	var result jsonOptions

	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&result); err != nil {
			return jsonOptions{}, err
		}
	}

	return result, nil
}

func revealJSONSecrets(value reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	if !value.IsValid() {
		return false, nil
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return false, nil
		}

		return revealJSONSecrets(value.Elem(), secretPassphrase, path)
	case reflect.Struct:
		return revealStructJSONSecrets(value, secretPassphrase, path)
	default:
		return false, nil
	}
}

func revealStructJSONSecrets(structElem reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	var updated bool

	for i := 0; i < structElem.NumField(); i++ {
		fieldType := structElem.Type().Field(i)
		if !fieldType.IsExported() {
			continue
		}

		fieldPath := joinFieldPath(path, fieldType.Name)
		field := structElem.Field(i)

		if hasJSONSecretTag(fieldType) {
			fieldUpdated, err := revealJSONSecretValue(field, secretPassphrase, fieldPath)
			if err != nil {
				return false, err
			}

			updated = updated || fieldUpdated
		}

		nestedUpdated, err := revealNestedJSONSecrets(field, secretPassphrase, fieldPath)
		if err != nil {
			return false, err
		}

		updated = updated || nestedUpdated
	}

	return updated, nil
}

func revealNestedJSONSecrets(field reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	if supportsTextUnmarshaler(field) {
		return false, nil
	}

	switch field.Kind() {
	case reflect.Struct:
		return revealStructJSONSecrets(field, secretPassphrase, path)
	case reflect.Pointer:
		if field.IsNil() {
			return false, nil
		}
		if field.Type().Elem().Kind() != reflect.Struct || field.Type().Implements(textUnmarshalerType) {
			return false, nil
		}

		return revealStructJSONSecrets(field.Elem(), secretPassphrase, path)
	default:
		return false, nil
	}
}

func hasJSONSecretTag(fieldType reflect.StructField) bool {
	tagValue, ok := fieldType.Tag.Lookup(jsonSecretTag)
	if !ok {
		return false
	}

	tagValue = strings.TrimSpace(tagValue)
	if tagValue == "-" {
		return false
	}

	return tagValue == "" ||
		tagValue == "1" ||
		strings.EqualFold(tagValue, "true") ||
		strings.EqualFold(tagValue, "yes")
}

func revealJSONSecretValue(field reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	if !field.IsValid() {
		return false, nil
	}

	switch field.Kind() {
	case reflect.String:
		return revealJSONStringSecret(field, secretPassphrase, path)
	case reflect.Pointer:
		if field.IsNil() {
			return false, nil
		}
		if field.Type().Elem().Kind() == reflect.Struct {
			return false, fmt.Errorf("config: field %s: secret tag is not supported on %s", path, field.Type())
		}

		return revealJSONSecretValue(field.Elem(), secretPassphrase, path)
	case reflect.Slice, reflect.Array:
		var updated bool

		for i := 0; i < field.Len(); i++ {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			itemUpdated, err := revealJSONSecretValue(field.Index(i), secretPassphrase, itemPath)
			if err != nil {
				return false, err
			}

			updated = updated || itemUpdated
		}

		return updated, nil
	default:
		return false, fmt.Errorf("config: field %s: secret tag is only supported on string values, pointers to string, or slices/arrays of them", path)
	}
}

func revealJSONStringSecret(field reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	value := field.String()
	if value == "" {
		return false, nil
	}

	plaintext, encrypted, err := decryptJSONSecret(value, secretPassphrase)
	if err != nil {
		return false, fmt.Errorf("config: field %s: %w", path, err)
	}
	if !encrypted {
		return true, nil
	}

	field.SetString(plaintext)
	return false, nil
}

func marshalEncryptedJSON(structure interface{}, secretPassphrase []byte) ([]byte, error) {
	clone, err := cloneJSONStructure(structure)
	if err != nil {
		return nil, err
	}

	if _, err := encryptJSONSecrets(reflect.ValueOf(clone).Elem(), secretPassphrase, ""); err != nil {
		return nil, err
	}

	return json.MarshalIndent(clone, "", "  ")
}

func cloneJSONStructure(structure interface{}) (interface{}, error) {
	data, err := json.Marshal(structure)
	if err != nil {
		return nil, err
	}

	clone := reflect.New(reflect.TypeOf(structure).Elem())
	if err := json.Unmarshal(data, clone.Interface()); err != nil {
		return nil, err
	}

	return clone.Interface(), nil
}

func encryptJSONSecrets(value reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	if !value.IsValid() {
		return false, nil
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return false, nil
		}

		return encryptJSONSecrets(value.Elem(), secretPassphrase, path)
	case reflect.Struct:
		return encryptStructJSONSecrets(value, secretPassphrase, path)
	default:
		return false, nil
	}
}

func encryptStructJSONSecrets(structElem reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	var updated bool

	for i := 0; i < structElem.NumField(); i++ {
		fieldType := structElem.Type().Field(i)
		if !fieldType.IsExported() {
			continue
		}

		fieldPath := joinFieldPath(path, fieldType.Name)
		field := structElem.Field(i)

		if hasJSONSecretTag(fieldType) {
			fieldUpdated, err := encryptJSONSecretValue(field, secretPassphrase, fieldPath)
			if err != nil {
				return false, err
			}

			updated = updated || fieldUpdated
		}

		nestedUpdated, err := encryptNestedJSONSecrets(field, secretPassphrase, fieldPath)
		if err != nil {
			return false, err
		}

		updated = updated || nestedUpdated
	}

	return updated, nil
}

func encryptNestedJSONSecrets(field reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	if supportsTextUnmarshaler(field) {
		return false, nil
	}

	switch field.Kind() {
	case reflect.Struct:
		return encryptStructJSONSecrets(field, secretPassphrase, path)
	case reflect.Pointer:
		if field.IsNil() {
			return false, nil
		}
		if field.Type().Elem().Kind() != reflect.Struct || field.Type().Implements(textUnmarshalerType) {
			return false, nil
		}

		return encryptStructJSONSecrets(field.Elem(), secretPassphrase, path)
	default:
		return false, nil
	}
}

func encryptJSONSecretValue(field reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	if !field.IsValid() {
		return false, nil
	}

	switch field.Kind() {
	case reflect.String:
		return encryptJSONStringSecret(field, secretPassphrase, path)
	case reflect.Pointer:
		if field.IsNil() {
			return false, nil
		}
		if field.Type().Elem().Kind() == reflect.Struct {
			return false, fmt.Errorf("config: field %s: secret tag is not supported on %s", path, field.Type())
		}

		return encryptJSONSecretValue(field.Elem(), secretPassphrase, path)
	case reflect.Slice, reflect.Array:
		var updated bool

		for i := 0; i < field.Len(); i++ {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			itemUpdated, err := encryptJSONSecretValue(field.Index(i), secretPassphrase, itemPath)
			if err != nil {
				return false, err
			}

			updated = updated || itemUpdated
		}

		return updated, nil
	default:
		return false, fmt.Errorf("config: field %s: secret tag is only supported on string values, pointers to string, or slices/arrays of them", path)
	}
}

func encryptJSONStringSecret(field reflect.Value, secretPassphrase []byte, path string) (bool, error) {
	value := field.String()
	if value == "" {
		return false, nil
	}

	encryptedValue, err := encryptJSONSecret(value, secretPassphrase)
	if err != nil {
		return false, fmt.Errorf("config: field %s: %w", path, err)
	}

	field.SetString(encryptedValue)
	return true, nil
}

func encryptJSONSecret(value string, secretPassphrase []byte) (string, error) {
	encryptedValue, err := secure.AESEncryptWithGCM([]byte(value), secretPassphrase)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt secret: %w", err)
	}

	return jsonSecretPrefix + string(encryptedValue), nil
}

func decryptJSONSecret(value string, secretPassphrase []byte) (string, bool, error) {
	if !strings.HasPrefix(value, jsonSecretPrefix) {
		return value, false, nil
	}

	plaintext, err := secure.AESDecryptWithGCM([]byte(strings.TrimPrefix(value, jsonSecretPrefix)), secretPassphrase)
	if err != nil {
		return "", true, fmt.Errorf("failed to decrypt secret: %w", err)
	}

	return string(plaintext), true, nil
}

func normalizeJSONLineEndings(data []byte, original []byte) []byte {
	if bytes.Contains(original, []byte("\r\n")) {
		return bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}

	return data
}
