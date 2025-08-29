package kwg

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/domino14/word-golib/cache"
	"github.com/domino14/word-golib/config"
	"github.com/domino14/word-golib/tilemapping"
)

const (
	CacheKeyPrefixKWG  = "kwg:"
	CacheKeyPrefixKBWG = "kbwg:"
)

// LoadWordGraph loads either a KWG or KBWG based on the file extension
func LoadWordGraph[T WordGraphConstraint](cfg *config.Config, filename string) (T, error) {
	log.Debug().Msgf("Loading %v ...", filename)
	file, filesize, err := cache.Open(filename)
	var result T
	if err != nil {
		return result, err
	}
	defer file.Close()

	// Determine if it's a KWG or KBWG file based on extension
	switch any(result).(type) {
	case *KBWG:
		kbwg, err := ScanKBWG(file, filesize)
		if err != nil {
			return result, err
		}
		result = any(kbwg).(T)
	case *KWG:
		kwg, err := ScanKWG(file, filesize)
		if err != nil {
			return result, err
		}
		result = any(kwg).(T)
	default:
		return result, errors.New("unsupported graph type for loading")
	}

	// Set lexicon name and alphabet
	lexfile := filepath.Base(filename)
	lexname, found := strings.CutSuffix(lexfile, filepath.Ext(lexfile))
	if !found {
		return result, errors.New("filename not in correct format")
	}

	// We need to set these fields, but the interface doesn't have setters
	// We need to use type assertions to access the underlying types
	switch v := any(result).(type) {
	case *KWG:
		v.lexiconName = lexname
		ld, err := tilemapping.ProbableLetterDistribution(cfg, lexname)
		if err != nil {
			return result, err
		}
		v.alphabet = ld.TileMapping()
	case *KBWG:
		v.lexiconName = lexname
		ld, err := tilemapping.ProbableLetterDistribution(cfg, lexname)
		if err != nil {
			return result, err
		}
		v.alphabet = ld.TileMapping()
	}

	return result, nil
}

// LoadKWG loads a KWG from a file (for backward compatibility)
func LoadKWG(cfg *config.Config, filename string) (*KWG, error) {
	return LoadWordGraph[*KWG](cfg, filename)
}

// LoadKBWG loads a KBWG from a file
func LoadKBWG(cfg *config.Config, filename string) (*KBWG, error) {
	return LoadWordGraph[*KBWG](cfg, filename)
}

// CacheLoadFuncKWG is the function that loads a KWG into the global cache
func CacheLoadFuncKWG(cfg *config.Config, key string) (interface{}, error) {
	lexiconName := strings.TrimPrefix(key, CacheKeyPrefixKWG)
	dataPath := cfg.DataPath
	kwgPrefix := cfg.KWGPathPrefix

	var result interface{}
	var err error

	if kwgPrefix == "" {
		result, err = LoadWordGraph[*KWG](cfg, filepath.Join(dataPath, "lexica", "gaddag", lexiconName+".kwg"))
	} else {
		result, err = LoadWordGraph[*KWG](cfg, filepath.Join(dataPath, "lexica", "gaddag", kwgPrefix, lexiconName+".kwg"))
	}

	return result, err
}

// CacheLoadFuncKBWG is the function that loads a KBWG into the global cache
func CacheLoadFuncKBWG(cfg *config.Config, key string) (interface{}, error) {
	lexiconName := strings.TrimPrefix(key, CacheKeyPrefixKBWG)
	dataPath := cfg.DataPath
	kwgPrefix := cfg.KWGPathPrefix

	var result interface{}
	var err error

	if kwgPrefix == "" {
		result, err = LoadWordGraph[*KBWG](cfg, filepath.Join(dataPath, "lexica", "gaddag", lexiconName+".kbwg"))
	} else {
		result, err = LoadWordGraph[*KBWG](cfg, filepath.Join(dataPath, "lexica", "gaddag", kwgPrefix, lexiconName+".kbwg"))
	}

	return result, err
}

func GetGraph[T WordGraphConstraint](cfg *config.Config, name string) (T, error) {
	var result T
	switch any(result).(type) {
	case *KWG:
		k, err := GetKWG(cfg, name)
		if err != nil {
			return result, err
		}
		result = any(k).(T)
	case *KBWG:
		kb, err := GetKBWG(cfg, name)
		if err != nil {
			return result, err
		}
		result = any(kb).(T)
	default:
		return result, errors.New("unsupported graph type")
	}
	return result, nil
}

// GetKWG loads a named KWG from the cache or from a file (for backward compatibility)
func GetKWG(cfg *config.Config, name string) (*KWG, error) {
	key := CacheKeyPrefixKWG + name
	obj, err := cache.Load(cfg, key, CacheLoadFuncKWG)
	if err != nil {
		return nil, err
	}

	kwg, ok := obj.(*KWG)
	if !ok {
		return nil, errors.New("could not convert cached object to KWG")
	}
	return kwg, nil
}

// GetKBWG loads a named KBWG from the cache or from a file
func GetKBWG(cfg *config.Config, name string) (*KBWG, error) {
	key := CacheKeyPrefixKBWG + name
	obj, err := cache.Load(cfg, key, CacheLoadFuncKBWG)
	if err != nil {
		return nil, err
	}

	kbwg, ok := obj.(*KBWG)
	if !ok {
		return nil, errors.New("could not convert cached object to KBWG")
	}
	return kbwg, nil
}
