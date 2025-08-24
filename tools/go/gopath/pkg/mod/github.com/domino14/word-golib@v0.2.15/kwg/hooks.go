package kwg

import (
	"github.com/domino14/word-golib/tilemapping"
)

const (
	// BackHooks are letters that hook on to the back of a word to make a new word
	BackHooks = iota
	// FrontHooks are letters that hook on to the front of a word to make a new word
	FrontHooks
	// A BackInnerHook is a boolean indicating whether the word would still be valid
	// if the last letter of it was removed.
	BackInnerHook
	// A FrontInnerHook is a boolean indicating whether the word would still be valid
	// if the first letter of it was removed.
	FrontInnerHook
)

func reverse(w []tilemapping.MachineLetter) []tilemapping.MachineLetter {
	wc := make([]tilemapping.MachineLetter, len(w))
	copy(wc, w)

	for i, j := 0, len(wc)-1; i < j; i, j = i+1, j-1 {
		wc[i], wc[j] = wc[j], wc[i]
	}
	return wc
}

func FindHooks(kwg *KWG, word []tilemapping.MachineLetter, hooktype int) []tilemapping.MachineLetter {
	nodeIdx := kwg.ArcIndex(1)
	if hooktype == BackHooks {
		// ArcIndex 0 is the dawg, can search directly.
		nodeIdx = kwg.ArcIndex(0)
	} else if hooktype == FrontHooks {
		word = reverse(word)
	}

	hooks := []tilemapping.MachineLetter{}

	lidx := 0
	for {
		if lidx > len(word)-1 {
			// If we've gone too far the word is not found.
			return nil
		}
		ml := word[lidx]
		if kwg.Tile(nodeIdx) == uint8(ml) {
			if lidx == len(word)-1 {
				if kwg.Accepts(nodeIdx) {
					// yay we're here
					break
				}
			}
			nodeIdx = kwg.ArcIndex(nodeIdx)
			lidx++
		} else {
			if kwg.IsEnd(nodeIdx) {
				return nil
			}
			nodeIdx++
		}
	}

	// if we made it here, the word was found. enumerate all next nodes that end.
	nodeIdx = kwg.ArcIndex(nodeIdx)
	for {
		if kwg.Accepts(nodeIdx) {
			hooks = append(hooks, tilemapping.MachineLetter(kwg.Tile(nodeIdx)))
		}
		if kwg.IsEnd(nodeIdx) {
			break
		}
		nodeIdx++
	}
	return hooks
}

func FindInnerHook(kwg *KWG, word []tilemapping.MachineLetter, hooktype int) bool {
	// use the dawg to just find a partial word.
	nodeIdx := kwg.ArcIndex(0)
	var tofind []tilemapping.MachineLetter
	if hooktype == FrontInnerHook {
		tofind = word[1:]
	} else if hooktype == BackInnerHook {
		tofind = word[:len(word)-1]
	}
	return findMachineWord(kwg, nodeIdx, tofind)
}
