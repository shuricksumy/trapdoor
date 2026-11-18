package clientauth

import "github.com/shuricksumy/tunnelward/internal/ccd"

// Store reads and writes the 2FA lines of ccd files.
type Store struct {
	Files *ccd.Store
}

// Get returns the settings of name (empty settings if it has no ccd file).
func (s *Store) Get(name string) (Settings, error) {
	data, err := s.Files.Read(name)
	if err != nil {
		return Settings{}, err
	}
	return Parse(data)
}

// Update changes the settings of name with fn, keeping all other lines.
func (s *Store) Update(name string, fn func(*Settings) error) (Settings, error) {
	var cur Settings
	err := s.Files.Edit(name, func(data string) (string, error) {
		var err error
		if cur, err = Parse(data); err != nil {
			return "", err
		}
		if err := fn(&cur); err != nil {
			return "", err
		}
		return Apply(data, cur), nil
	})
	return cur, err
}
