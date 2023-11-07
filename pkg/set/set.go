package set

import "sync"

type Set[T comparable] struct {
	mu sync.Mutex
	ts map[T]struct{}
}

func New[T comparable]() *Set[T] {
	return &Set[T]{
		ts: map[T]struct{}{},
	}
}

func (s *Set[T]) Add(t T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ts[t] = struct{}{}
}

func (s *Set[T]) Remove(t T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.ts, t)
}

func (s *Set[T]) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.ts)
}

func (s *Set[T]) Values() []T {
	var ts []T
	for t := range s.ts {
		ts = append(ts, t)
	}
	return ts
}
