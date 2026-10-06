package delivery

import "image"

// Codec encodes and decodes one wire format. Implementations must be
// deterministic: the same image and quality always produce the same bytes.
type Codec interface {
	Format() Format
	Encode(img image.Image, quality int) ([]byte, error)
	Decode(data []byte) (image.Image, error)
}

// codecRegistry maps formats to codecs. Populated in init.
var codecRegistry = map[Format]Codec{}

// registerCodec adds a codec; called from init functions.
func registerCodec(c Codec) { codecRegistry[c.Format()] = c }

// codecFor returns the codec for f or nil.
func codecFor(f Format) Codec { return codecRegistry[f] }

// efficiencyRank orders formats by typical delivered size; lower is
// smaller and therefore preferred during negotiation.
func efficiencyRank(f Format) int {
	switch f {
	case FormatRIMG:
		return 0
	case FormatJPEG:
		return 1
	case FormatPNG:
		return 2
	}
	return 100
}
