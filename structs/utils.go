package structs

import "iter"

type Location struct {
	Container *Object
	Content   map[string]*Object
}

func (l *Location) Inspect(viewer *Object) (*Description, Exits, Objects) {
	siblings := Objects{}
	for _, cont := range l.Content {
		if desc, _ := cont.Inspect(viewer); desc != nil {
			cont.Descriptions = []Description{*desc}
			siblings = append(siblings, *cont)
		}
	}
	desc, exits := l.Container.Inspect(viewer)
	return desc, exits, siblings
}

func (l *Location) All() iter.Seq2[string, *Object] {
	return func(yield func(string, *Object) bool) {
		if !yield(l.Container.Id, l.Container) {
			return
		}
		for k, v := range l.Content {
			if !yield(k, v) {
				return
			}
		}
	}
}

type Neighbourhood struct {
	Self       *Location
	Location   *Location
	Neighbours map[string]*Location
}

func (n *Neighbourhood) All() iter.Seq2[string, *Object] {
	return func(yield func(string, *Object) bool) {
		for k, v := range n.Location.All() {
			if !yield(k, v) {
				return
			}
		}
		for _, loc := range n.Neighbours {
			for k, v := range loc.All() {
				if !yield(k, v) {
					return
				}
			}
		}
	}
}
