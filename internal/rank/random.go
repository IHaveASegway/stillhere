package rank

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"math/rand/v2"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Seed derives the bootstrap seed from the metric, scope and window: the
// first 8 bytes, big-endian, of a SHA-256 over a length-prefixed encoding of
// the metric's name and version, the scope's kind and key, and the window's
// two instants. The same inputs give the same seed on every platform and Go
// release, a window's location does not change it, and a new metric version,
// another scope or another window gives an unrelated one.
func Seed(id domain.MetricID, scope domain.Scope, w domain.Window) uint64 {
	b := appendString(nil, "stillhere/rank.Seed/v1")
	b = appendString(b, id.Name)
	b = binary.BigEndian.AppendUint64(b, uint64(id.Version))
	b = appendString(b, scope.Kind)
	b = appendString(b, scope.Key)
	b = appendTime(b, w.Since)
	b = appendTime(b, w.Until)
	sum := sha256.Sum256(b)
	return binary.BigEndian.Uint64(sum[:8])
}

// newStream returns the random stream that labels name under seed: a PCG
// (math/rand/v2's PCG-DXSM, a published algorithm whose output Go's own
// tests pin from release to release) seeded with the first 16 bytes of a
// SHA-256 over the seed and the labels. Hashing
// derives as many independent streams from one seed as there are names (a
// splittable generator: Claessen and Pałka, "Splittable Pseudorandom Number
// Generators using Cryptographic Hashing", Haskell Symposium 2013), so a
// subject's draws do not depend on which other subjects are being ranked.
func newStream(seed uint64, labels ...string) *rand.PCG {
	b := appendString(nil, "stillhere/rank.stream/v1")
	b = binary.BigEndian.AppendUint64(b, seed)
	for _, l := range labels {
		b = appendString(b, l)
	}
	sum := sha256.Sum256(b)
	return rand.NewPCG(binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16]))
}

// bounded returns a uniform integer in [0, n) from src, for n > 0, by
// Lemire's multiply-and-reject method (Lemire, "Fast Random Integer
// Generation in an Interval", ACM TOMACS 29(1), 2019): the draw is the high
// word of x·n, and the x whose low word falls below 2⁶⁴ mod n are drawn
// again, which removes the bias of x mod n. rand.Rand.IntN computes much the
// same today, but its algorithm is not part of its documented API, and these
// draws decide published intervals, so the draw is this package's own.
func bounded(src *rand.PCG, n uint64) uint64 {
	hi, lo := bits.Mul64(src.Uint64(), n)
	if lo < n {
		threshold := -n % n // 2⁶⁴ mod n, in 64-bit arithmetic
		for lo < threshold {
			hi, lo = bits.Mul64(src.Uint64(), n)
		}
	}
	return hi
}

// appendString appends s after its length, so that no two sequences of
// fields encode to the same bytes.
func appendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint64(b, uint64(len(s)))
	return append(b, s...)
}

// appendTime appends t's instant, which does not depend on t's location.
func appendTime(b []byte, t time.Time) []byte {
	b = binary.BigEndian.AppendUint64(b, uint64(t.Unix()))
	return binary.BigEndian.AppendUint32(b, uint32(t.Nanosecond()))
}
