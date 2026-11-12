package money

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
)

var literalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]{1,6})?$`)

func Parse(value string) (*big.Rat, error) {
	value = strings.TrimSpace(value)
	if !literalPattern.MatchString(value) {
		return nil, errors.New("invalid decimal")
	}
	parsed, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, errors.New("invalid decimal")
	}
	return parsed, nil
}

func Add(values ...string) (string, error) {
	total := new(big.Rat)
	for _, value := range values {
		parsed, err := Parse(value)
		if err != nil {
			return "", err
		}
		total.Add(total, parsed)
	}
	return Format(total, 6), nil
}

func Sub(left, right string) (string, error) {
	leftValue, err := Parse(left)
	if err != nil {
		return "", err
	}
	rightValue, err := Parse(right)
	if err != nil {
		return "", err
	}
	return Format(new(big.Rat).Sub(leftValue, rightValue), 6), nil
}

func Mul(left, right string) (string, error) {
	leftValue, err := Parse(left)
	if err != nil {
		return "", err
	}
	rightValue, err := Parse(right)
	if err != nil {
		return "", err
	}
	return Format(new(big.Rat).Mul(leftValue, rightValue), 6), nil
}

func Round(value string, scale int) (string, error) {
	parsed, err := Parse(value)
	if err != nil {
		return "", err
	}
	return Format(parsed, scale), nil
}

func MulDivRound(left, numerator, denominator string, scale int) (string, error) {
	leftValue, err := Parse(left)
	if err != nil {
		return "", err
	}
	numeratorValue, err := Parse(numerator)
	if err != nil {
		return "", err
	}
	denominatorValue, err := Parse(denominator)
	if err != nil {
		return "", err
	}
	if denominatorValue.Sign() == 0 {
		return "", errors.New("division by zero")
	}
	result := new(big.Rat).Quo(new(big.Rat).Mul(leftValue, numeratorValue), denominatorValue)
	return Format(result, scale), nil
}

func Format(value *big.Rat, scale int) string {
	formatted := value.FloatString(scale)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	if formatted == "" || formatted == "-0" {
		return "0"
	}
	return formatted
}

func NonNegative(value string) bool {
	parsed, err := Parse(value)
	return err == nil && parsed.Sign() >= 0
}

func Positive(value string) bool {
	parsed, err := Parse(value)
	return err == nil && parsed.Sign() > 0
}

func Rate(value string) bool {
	parsed, err := Parse(value)
	if err != nil {
		return false
	}
	return parsed.Sign() >= 0 && parsed.Cmp(big.NewRat(1, 1)) <= 0
}
