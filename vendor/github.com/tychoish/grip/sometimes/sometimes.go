package sometimes

import (
	"math/rand"
	"time"
)

func init() {
	rand.Seed(time.Now().Unix())
}

func getRandNumber() int {
	return rand.Intn(101)
}

// Fifth returns true 20% of the time.
func Fifth() bool {
	return getRandNumber() > 80
}

// Half returns true 50% of the time.
func Half() bool {
	return getRandNumber() > 50
}

// Third returns true 33% of the time.
func Third() bool {
	return getRandNumber() > 67
}

// Quarter returns true 25% of the time.
func Quarter() bool {
	return getRandNumber() > 75
}

// ThreeQuarters returns true 75% of the time.
func ThreeQuarters() bool {
	return getRandNumber() > 25
}

// TwoThirds returns true 66% of the time.
func TwoThirds() bool {
	return getRandNumber() > 34
}
