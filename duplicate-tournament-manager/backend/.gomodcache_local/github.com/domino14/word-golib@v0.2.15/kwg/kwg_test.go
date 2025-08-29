package kwg

import (
	"testing"

	"github.com/domino14/word-golib/config"
	"github.com/matryer/is"
)

func TestLoadKWG(t *testing.T) {
	is := is.New(t)
	kwg, err := GetKWG(config.DefaultConfig, "NWL20")
	is.NoErr(err)
	is.Equal(len(kwg.nodes), 855967)
}
