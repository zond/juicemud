package storage

import (
	"math"

	"github.com/timshannon/badgerhold/v2"
)

type Object interface {
	Location() (Object, error)
	Name(definite bool) (string, error)
	ShortDescription() (string, error)
	LongDescription() (string, error)
	Content() (Objects, error)
}

type Objects []Object

type Storage struct {
	db      *badgerhold.Store
	objects map[uint64]*object
}

func (s *Storage) LoadUser(username string) (*User, error) {
	res := &User{}
	if err := s.db.Get(username, res); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Storage) CreateUser(user *User) error {
	txn := s.db.Badger().NewTransaction(true)
	defer txn.Discard()

	code := &Code{
		Text: `
function name(definite) {
  if (definite) {
    return 'the spark of light';
  } else {
    return 'a spark of light';
  }
}
function shortDescription() {
  return 'A spark of divine light';
}
function longDescription() {
  return 'This spark of divine light represents the essense of a god of this world.';
}`,
	}
	if err := s.db.TxInsert(txn, badgerhold.NextSequence(), code); err != nil {
		return err
	}

	object := &object{
		CodeID: code.ID,
	}
	if err := s.db.TxInsert(txn, badgerhold.NextSequence(), object); err != nil {
		return err
	}

	user.ObjectID = object.ID
	if err := s.db.TxInsert(txn, user.Username, user); err != nil {
		return err
	}

	return txn.Commit()
}

func New(db *badgerhold.Store) (*Storage, error) {
	res := &Storage{
		db:      db,
		objects: map[uint64]*object{},
	}
	if err := res.initialize(); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Storage) initialize() error {
	txn := s.db.Badger().NewTransaction(true)
	defer txn.Discard()

	voidCode := &Code{}
	if err := s.db.Get(0, voidCode); err == badgerhold.ErrNotFound {
		voidCode.Text = `
function name(definite) {
  if (definite) {
    return 'the void';
  } else {
    return 'a void';
  };
}
function shortDescription() {
  return 'The infinite void';
}
function longDescription() {
  return 'Cosmic desolation disturbed only be the faint crackle of quantum foam.';
}`
		if err := s.db.TxInsert(txn, badgerhold.NextSequence(), voidCode); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	void := &object{
		LocationID: math.MaxUint64,
	}
	if err := s.db.Get(0, void); err == badgerhold.ErrNotFound {
		void.CodeID = voidCode.ID
		if err := s.db.TxInsert(txn, badgerhold.NextSequence(), void); err != nil {
			return err
		}
	}
	return txn.Commit()
}

func (s *Storage) GetObject(id uint64) (Object, error) {
	if res, found := s.objects[id]; found {
		return res, nil
	}
	res := &object{}
	if err := s.db.Get(id, res); err != nil {
		return nil, err
	}
	res.storage = s
	if err := res.reload(); err != nil {
		return nil, err
	}
	s.objects[res.ID] = res
	return res, nil
}
