package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// FromJSON loads the JSON configuration file from the path and tries to convert it into a structure.
func FromJSON(structure interface{}, path string) error {
	file, err := os.ReadFile(path)
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
				if err := populateStruct(structure, key, value); err != nil {
					return err
				}
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
				if err := populateStruct(structure, key, value); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// populateStruct tries to populate the input structure using the key and value strings.
func populateStruct(structure interface{}, dataKey string, dataValue string) error {
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

				if err := populateSlice(elemPtr, dataValue, valueSep); err != nil {
					return err
				}
			default:
				if err := populateField(elemPtr, dataValue); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// populateSlice splits sliceValue by valueSep and tries to populate the input slice.
func populateSlice(slicePtr interface{}, sliceValue string, valueSep string) error {
	slice := reflect.ValueOf(slicePtr).Elem()
	sliceType := slice.Type().Elem()

	parts := strings.Split(sliceValue, valueSep)

	for _, part := range parts {
		fieldPtr := reflect.New(sliceType)
		if err := populateField(fieldPtr.Interface(), part); err != nil {
			return err
		}

		slice.Set(reflect.Append(slice, fieldPtr.Elem()))
	}

	return nil
}

// populateField tries to populate the input field.
func populateField(fieldPtr interface{}, fieldValue string) error {
	field := reflect.ValueOf(fieldPtr).Elem()

	if !field.IsValid() {
		return fmt.Errorf("field is not valid")
	}
	if !field.CanSet() {
		return fmt.Errorf("field cannot be set")
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
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
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
