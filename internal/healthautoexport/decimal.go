package healthautoexport

import (
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"
)

const maxCanonicalDecimalBytes = 4096

var ErrInvalidDecimal = errors.New("invalid decimal")

var _ driver.Valuer = Decimal{}

func mustDecimal(value string) Decimal {
	decimal, err := ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return decimal
}

// Decimal is an exact, finite, canonical base-10 value. Its zero value is
// invalid. Use ParseDecimal for application values; parsed provider numbers
// use the same validation. Value returns the canonical string for pgx/database
// SQL numeric parameters without a float64 conversion.
type Decimal struct{ canonical string }

func ParseDecimal(value string) (Decimal, error) {
	if value == "" || len(value) > maxCanonicalDecimalBytes {
		return Decimal{}, ErrInvalidDecimal
	}
	original := value
	negative := false
	if value[0] == '-' {
		negative = true
		value = value[1:]
	}
	if value == "" || value[0] == '+' {
		return Decimal{}, ErrInvalidDecimal
	}

	mantissa, exponentText := value, ""
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		mantissa, exponentText = value[:index], value[index+1:]
		if exponentText == "" || strings.IndexAny(exponentText, "eE") >= 0 {
			return Decimal{}, ErrInvalidDecimal
		}
	}
	dot := strings.IndexByte(mantissa, '.')
	if dot >= 0 && strings.IndexByte(mantissa[dot+1:], '.') >= 0 {
		return Decimal{}, ErrInvalidDecimal
	}
	integer, fraction := mantissa, ""
	if dot >= 0 {
		integer, fraction = mantissa[:dot], mantissa[dot+1:]
	}
	if integer == "" || (dot >= 0 && fraction == "") || !decimalDigits(integer) || !decimalDigits(fraction) {
		return Decimal{}, ErrInvalidDecimal
	}
	if len(integer) > 1 && integer[0] == '0' {
		return Decimal{}, ErrInvalidDecimal
	}

	exponent := int64(0)
	if exponentText != "" {
		parsed, err := strconv.ParseInt(exponentText, 10, 32)
		if err != nil {
			return Decimal{}, ErrInvalidDecimal
		}
		exponent = parsed
	}
	digits := integer + fraction
	decimalPosition := int64(len(integer)) + exponent
	firstNonzero := strings.IndexAny(digits, "123456789")
	if firstNonzero < 0 {
		return Decimal{canonical: "0"}, nil
	}
	digits = digits[firstNonzero:]
	decimalPosition -= int64(firstNonzero)

	var canonical string
	switch {
	case decimalPosition <= 0:
		outputLen := int64(2+len(digits)) - decimalPosition
		if outputLen > maxCanonicalDecimalBytes {
			return Decimal{}, ErrInvalidDecimal
		}
		canonical = "0." + strings.Repeat("0", int(-decimalPosition)) + digits
	case decimalPosition >= int64(len(digits)):
		outputLen := decimalPosition
		if outputLen > maxCanonicalDecimalBytes {
			return Decimal{}, ErrInvalidDecimal
		}
		canonical = digits + strings.Repeat("0", int(decimalPosition)-len(digits))
	default:
		canonical = digits[:decimalPosition] + "." + digits[decimalPosition:]
	}
	if dot := strings.IndexByte(canonical, '.'); dot >= 0 {
		canonical = strings.TrimRight(canonical, "0")
		canonical = strings.TrimRight(canonical, ".")
	}
	if negative && canonical != "0" {
		canonical = "-" + canonical
	}
	if len(canonical) > maxCanonicalDecimalBytes || original == "" {
		return Decimal{}, ErrInvalidDecimal
	}
	return Decimal{canonical: canonical}, nil
}

func decimalDigits(value string) bool {
	for _, digit := range []byte(value) {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (d Decimal) String() string { return d.canonical }

func (d Decimal) IsValid() bool { return d.canonical != "" }

func (d Decimal) IsPositive() bool {
	return d.IsValid() && d.canonical != "0" && d.canonical[0] != '-'
}

// Value implements driver.Valuer using PostgreSQL numeric-compatible text.
func (d Decimal) Value() (driver.Value, error) {
	if !d.IsValid() {
		return nil, ErrInvalidDecimal
	}
	return d.canonical, nil
}

func (d Decimal) MarshalText() ([]byte, error) {
	if !d.IsValid() {
		return nil, ErrInvalidDecimal
	}
	return []byte(d.canonical), nil
}

func (d Decimal) MarshalJSON() ([]byte, error) {
	if !d.IsValid() {
		return nil, ErrInvalidDecimal
	}
	return []byte(d.canonical), nil
}

func (d Decimal) compareAbs(other Decimal) int {
	left, right := strings.TrimPrefix(d.canonical, "-"), strings.TrimPrefix(other.canonical, "-")
	leftInteger, leftFraction := decimalParts(left)
	rightInteger, rightFraction := decimalParts(right)
	if len(leftInteger) != len(rightInteger) {
		if len(leftInteger) < len(rightInteger) {
			return -1
		}
		return 1
	}
	if leftInteger != rightInteger {
		if leftInteger < rightInteger {
			return -1
		}
		return 1
	}
	width := max(len(leftFraction), len(rightFraction))
	leftFraction += strings.Repeat("0", width-len(leftFraction))
	rightFraction += strings.Repeat("0", width-len(rightFraction))
	if leftFraction < rightFraction {
		return -1
	}
	if leftFraction > rightFraction {
		return 1
	}
	return 0
}

func decimalParts(value string) (string, string) {
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		return value[:dot], value[dot+1:]
	}
	return value, ""
}
