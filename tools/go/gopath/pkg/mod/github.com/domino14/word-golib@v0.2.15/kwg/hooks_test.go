package kwg

import (
	"testing"

	"github.com/domino14/word-golib/config"
	"github.com/domino14/word-golib/tilemapping"
	"github.com/matryer/is"
)

func TestHooks(t *testing.T) {
	is := is.New(t)
	d, err := GetKWG(config.DefaultConfig, "CSW21")
	is.NoErr(err)

	// AE
	ae_front_hooks := FindHooks(d, []tilemapping.MachineLetter{1, 5}, FrontHooks)
	is.Equal(ae_front_hooks, []tilemapping.MachineLetter{2, 4, 6, 7, 8, 11, 13, 14, 19, 20, 22, 23, 25})
	ae_back_hooks := FindHooks(d, []tilemapping.MachineLetter{1, 5}, BackHooks)
	is.Equal(ae_back_hooks, []tilemapping.MachineLetter{})

	is.True(FindInnerHook(d, []tilemapping.MachineLetter{1, 5}, BackInnerHook) == false)
	is.True(FindInnerHook(d, []tilemapping.MachineLetter{1, 5}, FrontInnerHook) == false)

	// EA
	ea_front_hooks := FindHooks(d, []tilemapping.MachineLetter{5, 1}, FrontHooks)
	is.Equal(ea_front_hooks, []tilemapping.MachineLetter{11, 12, 16, 19, 20, 25, 26})
	ea_back_hooks := FindHooks(d, []tilemapping.MachineLetter{5, 1}, BackHooks)
	is.Equal(ea_back_hooks, []tilemapping.MachineLetter{14, 18, 19, 20, 21})

	is.True(FindInnerHook(d, []tilemapping.MachineLetter{5, 1}, BackInnerHook) == false)
	is.True(FindInnerHook(d, []tilemapping.MachineLetter{5, 1}, FrontInnerHook) == false)

	// CRAWL
	crawl_front_hooks := FindHooks(d, []tilemapping.MachineLetter{3, 18, 1, 23, 12}, FrontHooks)
	is.Equal(crawl_front_hooks, []tilemapping.MachineLetter{1, 19})
	crawl_back_hooks := FindHooks(d, []tilemapping.MachineLetter{3, 18, 1, 23, 12}, BackHooks)
	is.Equal(crawl_back_hooks, []tilemapping.MachineLetter{19, 25})

	is.True(FindInnerHook(d, []tilemapping.MachineLetter{3, 18, 1, 23, 12}, BackInnerHook))
	is.True(FindInnerHook(d, []tilemapping.MachineLetter{3, 18, 1, 23, 12}, FrontInnerHook) == false)

	// FADDY
	faddy_front_hooks := FindHooks(d, []tilemapping.MachineLetter{6, 1, 4, 4, 25}, FrontHooks)
	is.Equal(faddy_front_hooks, []tilemapping.MachineLetter{})
	faddy_back_hooks := FindHooks(d, []tilemapping.MachineLetter{6, 1, 4, 4, 25}, BackHooks)
	is.Equal(faddy_back_hooks, []tilemapping.MachineLetter{})

	is.True(FindInnerHook(d, []tilemapping.MachineLetter{6, 1, 4, 4, 25}, BackInnerHook) == false)
	is.True(FindInnerHook(d, []tilemapping.MachineLetter{6, 1, 4, 4, 25}, FrontInnerHook))

}
