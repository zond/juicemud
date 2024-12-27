package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	_ "modernc.org/sqlite"
)

type Storage struct {
	db *sqlx.DB
}

type User struct {
	Id           int64
	Name         string `db:"unique"`
	PasswordHash string
	Owner        bool `db:"index"`
}

type Group struct {
	Id         int64
	Name       string `db:"unique"`
	OwnerGroup int64
}

type GroupMember struct {
	Id   int64
	Name string
}

type Directory struct {
	Id         int64
	Parent     int64 `db:"index"`
	Name       string
	ReadGroup  int64
	WriteGroup int64
}

type File struct {
	Id         int64
	Directory  int64 `db:"index"`
	Name       string
	Content    string
	ReadGroup  int64
	WriteGroup int64
}

func New(path string) (*Storage, error) {
	db, err := sqlx.Connect("sqlite", path)
	if err != nil {
		return nil, err
	}
	result := &Storage{
		db: db,
	}
	if err := result.EnsureTables([]any{User{}, Group{}, GroupMember{}, Directory{}, File{}}); err != nil {
		return nil, err
	}
	return &Storage{
		db: db,
	}, nil
}

var (
	sqlTypeMap = map[reflect.Kind]string{
		reflect.Int64:  "INTEGER",
		reflect.String: "TEXT",
		reflect.Bool:   "INTEGER",
	}
)

func (s *Storage) EnsureTables(prototypes []any) error {
	for _, prototype := range prototypes {
		if err := s.EnsureTable(prototype); err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) Insert(instance any) error {
	typ := reflect.TypeOf(instance)
	if typ.Kind() != reflect.Ptr {
		return fmt.Errorf("only pointers to structs can be updated")
	}
	typ = typ.Elem()
	if typ.Kind() != reflect.Struct {
		return fmt.Errorf("only points to structs can be updated")
	}
	val := reflect.ValueOf(instance).Elem()
	cols := []string{}
	qmarks := []string{}
	params := []any{}
	for fieldIndex := 0; fieldIndex < typ.NumField(); fieldIndex++ {
		field := typ.Field(fieldIndex)
		if field.Name != "Id" {
			cols = append(cols, field.Name)
			qmarks = append(qmarks, "?")
			params = append(params, val.FieldByName(field.Name).Interface())
		}
	}
	execResult, err := s.db.Exec(fmt.Sprintf("INSERT INTO `%s` (%s) VALUES (%s)", typ.Name(), strings.Join(cols, ","), strings.Join(qmarks, ",")), params...)
	if err != nil {
		return fmt.Errorf("failed inserting %+v: %v", instance, err)
	}
	lastInsertId, err := execResult.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed getting id of inserted %+v: %v", instance, err)
	}
	val.FieldByName("Id").SetInt(lastInsertId)
	return nil

}

func (s *Storage) Update(instance any) error {
	typ := reflect.TypeOf(instance)
	if typ.Kind() != reflect.Ptr {
		return fmt.Errorf("only pointers to structs can be updated")
	}
	typ = typ.Elem()
	if typ.Kind() != reflect.Struct {
		return fmt.Errorf("only points to structs can be updated")
	}
	val := reflect.ValueOf(instance).Elem()
	query := bytes.NewBufferString(fmt.Sprintf("UPDATE `%s` SET ", typ.Name()))
	params := []any{}
	for fieldIndex := 0; fieldIndex < typ.NumField(); fieldIndex++ {
		field := typ.Field(fieldIndex)
		if field.Name != "Id" {
			if len(params) > 0 {
				fmt.Fprintf(query, ", ")
			}
			fmt.Fprintf(query, "`%s` = ?", field.Name)
			params = append(params, val.FieldByName(field.Name).Interface())
		}
	}
	fmt.Fprintf(query, " WHERE `Id` = ?")
	params = append(params, val.FieldByName("Id").Interface())
	log.Printf("going to exec %q", query.String())
	_, err := s.db.Exec(query.String(), params...)
	if err != nil {
		return fmt.Errorf("failed updating %+v: %v", instance, err)
	}
	return nil
}

func (s *Storage) EnsureTable(prototype any) error {
	typ := reflect.TypeOf(prototype)
	if typ.Kind() != reflect.Struct {
		return fmt.Errorf("only plain structs can be table prototypes")
	}
	if field, found := typ.FieldByName("Id"); !found {
		return fmt.Errorf("only structs with an Id field can be table prototypes")
	} else if field.Type.Kind() != reflect.Int64 {
		return fmt.Errorf("only structs with an Id field which is an int64 can be table prototypes")
	} else if _, err := s.db.Exec(fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` (Id INTEGER PRIMARY KEY)", typ.Name())); err != nil {
		return fmt.Errorf("unable to ensure existence of %s table: %v", typ.Name(), err)
	}
	for fieldIndex := 0; fieldIndex < typ.NumField(); fieldIndex++ {
		field := typ.Field(fieldIndex)
		if sqlType, found := sqlTypeMap[field.Type.Kind()]; found {
			if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `%s` %s", typ.Name(), field.Name, sqlType)); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("unable to ensure existence of %s.%s: %v", typ.Name(), field.Name, err)
			}
		} else {
			return fmt.Errorf("only kinds in %+v are allowed fields in table prototypes", sqlTypeMap)
		}
		tag := field.Tag.Get("db")
		if tag == "index" || tag == "unique" {
			unique := ""
			if tag == "unique" {
				unique = "UNIQUE "
			}
			if _, err := s.db.Exec(fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS `%s.%s` ON `%s` (`%s`)", unique, typ.Name(), field.Name, typ.Name(), field.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Storage) LoadUser(name string) (*User, error) {
	result := &User{}
	if err := s.db.Get(result, "SELECT * FROM User WHERE NAME = ?", name); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Storage) LoadSource(id uuid.UUID) (string, error) {
	return s.loadSource(id.String())
}

func (s *Storage) loadSource(id string) (string, error) {
	result := ""
	if err := s.db.Get(&result, "SELECT CONTENT FROM SOURCE WHERE UUID = ?", id); err != nil {
		return "", err
	}
	return result, nil
}

func (s *Storage) SaveSource(id uuid.UUID, content string) error {
	_, err := s.db.Exec("UPDATE SOURCE SET CONTENT = ? WHERE UUID = ?", content, id.String())
	return err
}

func (s *Storage) LoadRootState() (map[string]any, error) {
	return s.loadState("root")
}

func (s *Storage) LoadState(id uuid.UUID) (map[string]any, error) {
	return s.loadState(id.String())
}

func (s *Storage) loadState(id string) (map[string]any, error) {
	result := map[string]any{}
	resultJSON := ""
	if err := s.db.Get(&resultJSON, "SELECT CONTENT FROM STATE WHERE UUID = ?", id); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Storage) SaveState(id uuid.UUID, content string) error {
	contentJSONBytes, err := json.Marshal(content)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE STATE SET CONTENT = ? WHERE UUID = ?", string(contentJSONBytes), id.String())
	return err
}
