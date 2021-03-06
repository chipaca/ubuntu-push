/*
 Copyright 2013-2014 Canonical Ltd.

 This program is free software: you can redistribute it and/or modify it
 under the terms of the GNU General Public License version 3, as published
 by the Free Software Foundation.

 This program is distributed in the hope that it will be useful, but
 WITHOUT ANY WARRANTY; without even the implied warranties of
 MERCHANTABILITY, SATISFACTORY QUALITY, or FITNESS FOR A PARTICULAR
 PURPOSE.  See the GNU General Public License for more details.

 You should have received a copy of the GNU General Public License along
 with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

// levelmap holds an implementation of the LevelMap that the client
// session uses to keep track of what messages it has seen.
package levelmap

// This implementation is memory-based and does not save state. There
// is another one that stores the levels in sqlite that is missing a
// few dependencies still.

type LevelMap interface {
	// Set() (re)sets the given level to the given value.
	Set(level string, top int64)
	// GetAll() returns a "simple" map of the current levels.
	GetAll() map[string]int64
}

type mapLevelMap map[string]int64

func (m *mapLevelMap) Set(level string, top int64) {
	(*m)[level] = top
}
func (m *mapLevelMap) GetAll() map[string]int64 {
	return map[string]int64(*m)
}

var _ LevelMap = &mapLevelMap{}

// default constructor
func NewLevelMap() LevelMap {
	return &mapLevelMap{}
}
