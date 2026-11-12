package application

import (
	"math/big"

	"technical-service-backend/internal/money"
)

func addDecimal(values ...string) (string, error) {
	return money.Add(values...)
}

func subtractDecimal(left, right string) (string, error) {
	return money.Sub(left, right)
}

func multiplyDecimal(left, right string) (string, error) {
	return money.Mul(left, right)
}

func roundDecimal(value string, scale int) (string, error) {
	return money.Round(value, scale)
}

func parseDecimal(value string) (*big.Rat, error) {
	return money.Parse(value)
}

func validNonNegativeDecimal(value string) bool {
	return money.NonNegative(value)
}

func validPositiveDecimal(value string) bool {
	return money.Positive(value)
}

func validRate(value string) bool {
	return money.Rate(value)
}
