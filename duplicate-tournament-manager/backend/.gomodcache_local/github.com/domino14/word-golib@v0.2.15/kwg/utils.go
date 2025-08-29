package kwg

import (
	"github.com/domino14/word-golib/tilemapping"
	"github.com/rs/zerolog/log"
)

// FindWord finds a word in a KWG
func FindWord[T WordGraphConstraint](d T, word string) bool {
	return findWord(d, d.ArcIndex(0), word)
}

// FindMachineWord finds a word in a KWG
func FindMachineWord[T WordGraphConstraint](d T, word tilemapping.MachineWord) bool {
	return findMachineWord(d, d.ArcIndex(0), word)
}

func findWord[T WordGraphConstraint](d T, nodeIdx uint32, word string) bool {

	mw, err := tilemapping.ToMachineWord(word, d.GetAlphabet())
	if err != nil {
		log.Err(err).Msg("convert-to-mw")
		return false
	}
	return findMachineWord(d, nodeIdx, mw)
}

func findMachineWord[T WordGraphConstraint](d T, nodeIdx uint32, word tilemapping.MachineWord) bool {
	if len(word) < 2 {
		return false
	}
	lidx := 0

	for {
		if lidx > len(word)-1 {
			// If we've gone too far the word is not found.
			return false
		}
		letter := word[lidx]
		if d.Tile(nodeIdx) == uint8(letter) {
			if lidx == len(word)-1 {
				return d.Accepts(nodeIdx)
			}
			nodeIdx = d.ArcIndex(nodeIdx)
			lidx++
		} else {
			if d.IsEnd(nodeIdx) {
				return false
			}
			nodeIdx++
		}
	}
}
