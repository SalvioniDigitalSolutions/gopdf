//go:build race

package gopdf

// raceSlowdown: the race detector makes code run 5 to 20 times slower,
// and the time guards of the tests (within) are backstops against work
// that never ends, not measurements: under it they allow that much more.
const raceSlowdown = 10
