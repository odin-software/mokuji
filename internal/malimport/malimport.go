// Package malimport parsea el export de manga de MyAnimeList
// (myanimelist.net/panel.php?go=export → "Export My Manga List").
//
// Solo parsea: no sabe nada de la biblioteca ni de AniList. El merge con la
// biblioteca vive en internal/library (spec 2026-10-19-import-mal-design.md).
//
// El formato de tags es el supuesto del spec (§2): raíz <myanimelist>, una
// <manga> por entrada con manga_mangadb_id, manga_title, my_read_chapters y
// my_status. Todo lo demás se ignora.
package malimport

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	ErrInvalid        = errors.New("no es un export de MyAnimeList")
	ErrAnimeExport    = errors.New("es un export de anime, no de manga")
	ErrTooLarge       = errors.New("export demasiado grande")
	ErrTooManyEntries = errors.New("demasiadas entradas")
)

const (
	// MaxDecompressed acota lo que leemos después de descomprimir: un .gz de
	// pocos KB puede expandirse a gigas (gzip bomb).
	MaxDecompressed = 20 << 20
	// MaxEntries mantiene el import dentro del WriteTimeout del server
	// (spec §7: 1000 entradas = 20 llamadas a AniList).
	MaxEntries = 1000
)

// Entry es una entrada del export ya normalizada.
type Entry struct {
	MALID        int
	Title        string
	ReadChapters float64 // 0 si vacío, inválido o negativo
	Status       string  // "reading" | "planned" | "completed"; "" = Dropped, no importar
}

// MapStatus traduce el my_status de MAL al estado de mokuji (spec §4).
// Devuelve "" para Dropped: esas entradas no se importan.
func MapStatus(mal string) string {
	switch strings.TrimSpace(mal) {
	case "Reading":
		return "reading"
	case "Completed":
		return "completed"
	case "Dropped":
		return ""
	default: // Plan to Read, On-Hold y cualquier valor desconocido
		return "planned"
	}
}

type rawManga struct {
	ID           string `xml:"manga_mangadb_id"`
	Title        string `xml:"manga_title"`
	ReadChapters string `xml:"my_read_chapters"`
	Status       string `xml:"my_status"`
}

// Parse lee un export de MAL, plano o gzipeado (detectado por magic bytes,
// no por extensión). Los MAL ids repetidos conservan la primera aparición.
func Parse(r io.Reader) ([]Entry, error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip: %v", ErrInvalid, err)
		}
		defer zr.Close()
		src = zr
	}

	limited := &io.LimitedReader{R: src, N: MaxDecompressed + 1}
	dec := xml.NewDecoder(limited)

	var (
		entries  []Entry
		seen     = map[int]bool{}
		sawRoot  bool
		sawAnime bool
	)
	for {
		tok, err := dec.Token()
		if limited.N <= 0 {
			return nil, ErrTooLarge
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "myanimelist":
			sawRoot = true
		case "anime":
			sawAnime = true
			if err := dec.Skip(); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
		case "manga":
			var raw rawManga
			if err := dec.DecodeElement(&raw, &start); err != nil {
				if limited.N <= 0 {
					return nil, ErrTooLarge
				}
				return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			id, err := strconv.Atoi(strings.TrimSpace(raw.ID))
			if err != nil || id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			if len(entries) == MaxEntries {
				return nil, ErrTooManyEntries
			}
			entries = append(entries, Entry{
				MALID:        id,
				Title:        strings.TrimSpace(raw.Title),
				ReadChapters: parseChapters(raw.ReadChapters),
				Status:       MapStatus(raw.Status),
			})
		}
	}

	switch {
	case !sawRoot:
		return nil, ErrInvalid
	case len(entries) == 0 && sawAnime:
		return nil, ErrAnimeExport
	case len(entries) == 0:
		return nil, ErrInvalid
	}
	return entries, nil
}

func parseChapters(s string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
