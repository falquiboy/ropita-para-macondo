package tilemapping

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/domino14/word-golib/config"
)

// LetterDistribution encodes the tile distribution for the relevant game.
type LetterDistribution struct {
	tilemapping      *TileMapping
	Vowels           []MachineLetter
	distribution     []uint8
	scores           []int
	numUniqueLetters uint
	numLetters       uint
	Name             string
}

func ScanLetterDistribution(data io.Reader) (*LetterDistribution, error) {
	r := csv.NewReader(data)
	dist := []uint8{}
	ptValues := []int{}
	vowels := []MachineLetter{}
	alph := &TileMapping{}
	alph.Init()
	// letter,quantity,value,vowel
	idx := 0
	letters := []string{}
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		letter := record[0]
		n, err := strconv.Atoi(record[1])
		if err != nil {
			return nil, err
		}
		p, err := strconv.Atoi(record[2])
		if err != nil {
			return nil, err
		}
		v, err := strconv.Atoi(record[3])
		if err != nil {
			return nil, err
		}
		if v == 1 {
			vowels = append(vowels, MachineLetter(idx))
		}
		dist = append(dist, uint8(n))
		ptValues = append(ptValues, p)
		letters = append(letters, letter)
		idx++
	}
	alph.Reconcile(letters)
	return newLetterDistribution(alph, dist, ptValues, vowels), nil
}

func newLetterDistribution(alph *TileMapping, dist []uint8,
	ptValues []int, vowels []MachineLetter) *LetterDistribution {

	numTotalLetters := uint(0)
	numUniqueLetters := uint(len(dist))
	for _, v := range dist {
		numTotalLetters += uint(v)
	}
	// Note: numUniqueLetters/numTotalLetters includes the blank.

	return &LetterDistribution{
		tilemapping:      alph,
		distribution:     dist,
		scores:           ptValues,
		Vowels:           vowels,
		numUniqueLetters: numUniqueLetters,
		numLetters:       numTotalLetters,
	}

}

// Score gives the score of the given machine letter. This is used by the
// move generator to score plays more rapidly than looking up a map.
func (ld *LetterDistribution) Score(ml MachineLetter) int {
	if ml.IsBlanked() {
		return ld.scores[0] // the blank
	}
	return ld.scores[ml]
}

func (d *LetterDistribution) TileMapping() *TileMapping {
	return d.tilemapping
}

// Score returns the score of this word given the ld.
func (ld *LetterDistribution) WordScore(mw MachineWord) int {
	score := 0
	for _, c := range mw {
		score += ld.Score(c)
	}
	return score
}

func (ld *LetterDistribution) Distribution() []uint8 {
	return ld.distribution
}

// EnglishLetterDistribution returns the English letter distribution.
func EnglishLetterDistribution(cfg *config.Config) (*LetterDistribution, error) {
	return NamedLetterDistribution(cfg, "english")
}

// MakeBag returns a bag of tiles.
func (ld *LetterDistribution) MakeBag() *Bag {

	b := NewBag(ld, ld.tilemapping)
	b.Shuffle()

	return b
}

func (ld *LetterDistribution) NumTotalLetters() uint {
	return ld.numLetters
}

// ProbableLetterDistributionName returns a letter distribution name given a lexicon name.
// It makes a best guess for the letter distribution, assuming that it would be
// the "standard" one for that lexicon.
func ProbableLetterDistributionName(lexname string) (string, error) {
	lexname = strings.ToLower(lexname)

	var ldName string

	prefixMap := map[string]string{
		"nwl":     "english",
		"nswl":    "english",
		"twl":     "english",
		"owl":     "english",
		"csw":     "english",
		"america": "english",
		"cel":     "english",
		"ecwl":    "english",
		"iwi":     "english",
		"osps":    "polish",
		"nsf":     "norwegian",
		"fra":     "french",
		"rd":      "german",
		"deutsch": "german",
		"cgl":     "german",
		"disc":    "catalan",
		"fise":    "spanish",
		"file":    "spanish",
		"dsw":     "dutch",
	}
	for prefix, name := range prefixMap {
		if strings.HasPrefix(lexname, prefix) {
			ldName = name
			return ldName, nil
		}
	}

	return "", errors.New("cannot determine alphabet from lexicon name " + lexname)
}

// ProbableLetterDistribution returns a letter distribution given a lexicon name.
// It makes a best guess for the letter distribution, assuming that it would be
// the "standard" one for that lexicon.
func ProbableLetterDistribution(cfg *config.Config, lexname string) (*LetterDistribution, error) {
	ldName, err := ProbableLetterDistributionName(lexname)
	if err != nil {
		return nil, err
	}
	return NamedLetterDistribution(cfg, ldName)
}
