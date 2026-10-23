package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func time_(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

// TestTheViewModelFormats: amounts grouped at the currency's places, rates as percents, the total in capitals.
func TestTheViewModelFormats(t *testing.T) {
	assert.Equal(t, "1,234,567.50", grouped("1234567.5", 2))
	assert.Equal(t, "0.00", grouped("0", 2))
	assert.Equal(t, "120.125", grouped("120.125", 3))
	assert.Equal(t, "13%", percent("0.13"))
	assert.Equal(t, "6.5%", percent("0.065"))
	for amount, words := range map[string]string{
		"430.2":       "人民币肆佰叁拾元贰角整",
		"430.25":      "人民币肆佰叁拾元贰角伍分",
		"1000.05":     "人民币壹仟元零伍分",
		"100000001":   "人民币壹亿零壹元整",
		"1010000":     "人民币壹佰零壹万元整",
		"0.5":         "人民币伍角整",
		"0":           "人民币零元整",
		"10203.4":     "人民币壹万零贰佰零叁元肆角整",
		"12345678.90": "人民币壹仟贰佰叁拾肆万伍仟陆佰柒拾捌元玖角整",
	} {
		assert.Equal(t, words, inWords(amount, "CNY"), amount)
	}
}
