package mangadex

import "git.odin.do/kengru/mokuji/internal/releasesource"

// MangaResult y Chapter son aliases hacia los tipos canónicos del paquete
// releasesource. El cliente MangaDex es un releasesource.Client más; las
// firmas concretas usan estos aliases para no romper callers que ya
// importaban mangadex.MangaResult / mangadex.Chapter.
type (
	MangaResult = releasesource.MangaResult
	Chapter     = releasesource.Chapter
)
