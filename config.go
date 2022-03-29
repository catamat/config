package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// FromJSON loads the JSON configuration file from the path and tries to convert it into a structure.
func FromJSON(structure interface{}, path string) error {
	file, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	_, ok := structure.(*[]byte)
	if ok {
		*structure.(*[]byte) = file
	}

	if structure != nil {
		err = json.Unmarshal(file, structure)
		return err
	}

	return nil
}

// FromEnv loads the configuration vars from the environment and tries to convert them into a structure.
func FromEnv(structure interface{}) error {
	for _, s := range os.Environ() {
		if !strings.Contains(s, "=") {
			continue
		}

		parts := strings.SplitN(s, "=", 2)
		key, value := parts[0], parts[1]

		if key != "" && value != "" {
			if structure != nil {
				populateStruct(structure, key, value)
			}
		}
	}

	return nil
}

// FromFlags loads the configuration flags from the command-line and tries to convert them into a structure.
func FromFlags(structure interface{}) error {
	for _, s := range os.Args[1:] {
		if !strings.Contains(s, "=") {
			continue
		}

		parts := strings.SplitN(s, "=", 2)
		key, value := parts[0], parts[1]

		if key != "" && value != "" {
			if structure != nil {
				populateStruct(structure, key, value)
			}
		}
	}

	return nil
}

// populateStruct tries to populate the input structure using the key and value strings.
func populateStruct(structure interface{}, dataKey string, dataValue string) {
	structElem := reflect.ValueOf(structure).Elem()

	for i := 0; i < structElem.NumField(); i++ {
		fieldType := structElem.Type().Field(i)
		fieldName := fieldType.Name
		elemPtr := structElem.Field(i).Addr().Interface()

		if fieldType.Tag.Get("env") != "" {
			fieldName = fieldType.Tag.Get("env")
		}

		if fieldType.Tag.Get("flag") != "" {
			fieldName = fieldType.Tag.Get("flag")
		}

		if fieldName == dataKey {
			switch fieldType.Type.Kind() {
			//case reflect.Struct:
			//	...to do?
			//case reflect.Map:
			//	...to do?
			case reflect.Slice:
				valueSep := ":"

				if fieldType.Tag.Get("vsep") != "" {
					valueSep = fieldType.Tag.Get("vsep")
				}

				populateSlice(elemPtr, dataValue, valueSep)
			default:
				populateField(elemPtr, dataValue)
			}
		}
	}
}

// populateSlice splits sliceValue by valueSep and tries to populate the input slice.
func populateSlice(slicePtr interface{}, sliceValue string, valueSep string) {
	slice := reflect.ValueOf(slicePtr).Elem()
	sliceType := slice.Type().Elem()

	parts := strings.Split(sliceValue, valueSep)

	for _, part := range parts {
		fieldPtr := reflect.New(sliceType)
		populateField(fieldPtr.Interface(), part)
		slice.Set(reflect.Append(slice, fieldPtr.Elem()))
	}
}

// populateField tries to populate the input field.
func populateField(fieldPtr interface{}, fieldValue string) {
	field := reflect.ValueOf(fieldPtr).Elem()

	if field.IsValid() && field.CanSet() {
		switch field.Kind() {
		case reflect.String:
			field.SetString(fieldValue)
		case reflect.Int:
			r, _ := strconv.ParseInt(fieldValue, 10, 0)
			field.SetInt(r)
		case reflect.Int8:
			r, _ := strconv.ParseInt(fieldValue, 10, 8)
			field.SetInt(r)
		case reflect.Int16:
			r, _ := strconv.ParseInt(fieldValue, 10, 16)
			field.SetInt(r)
		case reflect.Int32:
			r, _ := strconv.ParseInt(fieldValue, 10, 32)
			field.SetInt(r)
		case reflect.Int64:
			r, _ := strconv.ParseInt(fieldValue, 10, 64)
			field.SetInt(r)
		case reflect.Uint:
			r, _ := strconv.ParseUint(fieldValue, 10, 0)
			field.SetUint(r)
		case reflect.Uint8:
			r, _ := strconv.ParseUint(fieldValue, 10, 8)
			field.SetUint(r)
		case reflect.Uint16:
			r, _ := strconv.ParseUint(fieldValue, 10, 16)
			field.SetUint(r)
		case reflect.Uint32:
			r, _ := strconv.ParseUint(fieldValue, 10, 32)
			field.SetUint(r)
		case reflect.Uint64:
			r, _ := strconv.ParseUint(fieldValue, 10, 64)
			field.SetUint(r)
		case reflect.Bool:
			r, _ := strconv.ParseBool(fieldValue)
			field.SetBool(r)
		case reflect.Float32:
			r, _ := strconv.ParseFloat(fieldValue, 32)
			field.SetFloat(r)
		case reflect.Float64:
			r, _ := strconv.ParseFloat(fieldValue, 64)
			field.SetFloat(r)
		default:
			panic(fmt.Sprintf("cannot handle field kind: %v", field.Type().Kind()))
		}
	}
}
