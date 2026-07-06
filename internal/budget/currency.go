package budget

import (
	"fmt"
	"sort"
	"strings"
)

// isoCurrencies is the set of all 162 active ISO 4217 currency codes.
// Used for validation when setting the target currency.
var isoCurrencies = map[string]bool{
	"AED": true, "AFN": true, "ALL": true, "AMD": true, "ANG": true, "AOA": true,
	"ARS": true, "AUD": true, "AWG": true, "AZN": true, "BAM": true, "BBD": true,
	"BDT": true, "BGN": true, "BHD": true, "BIF": true, "BMD": true, "BND": true,
	"BOB": true, "BOV": true, "BRL": true, "BSD": true, "BTN": true, "BWP": true,
	"BYN": true, "BZD": true, "CAD": true, "CDF": true, "CHE": true, "CHF": true,
	"CHW": true, "CLF": true, "CLP": true, "CNY": true, "COP": true, "COU": true,
	"CRC": true, "CUC": true, "CUP": true, "CVE": true, "CZK": true, "DJF": true,
	"DKK": true, "DOP": true, "DZD": true, "EGP": true, "ERN": true, "ETB": true,
	"EUR": true, "FJD": true, "FKP": true, "GBP": true, "GEL": true, "GHS": true,
	"GIP": true, "GMD": true, "GNF": true, "GTQ": true, "GYD": true, "HKD": true,
	"HNL": true, "HRK": true, "HTG": true, "HUF": true, "IDR": true, "ILS": true,
	"INR": true, "IQD": true, "IRR": true, "ISK": true, "JMD": true, "JOD": true,
	"JPY": true, "KES": true, "KGS": true, "KHR": true, "KMF": true, "KPW": true,
	"KRW": true, "KWD": true, "KYD": true, "KZT": true, "LAK": true, "LBP": true,
	"LKR": true, "LRD": true, "LSL": true, "LYD": true, "MAD": true, "MDL": true,
	"MGA": true, "MKD": true, "MMK": true, "MNT": true, "MOP": true, "MRU": true,
	"MUR": true, "MVR": true, "MWK": true, "MXN": true, "MXV": true, "MYR": true,
	"MZN": true, "NAD": true, "NGN": true, "NIO": true, "NOK": true, "NPR": true,
	"NZD": true, "OMR": true, "PAB": true, "PEN": true, "PGK": true, "PHP": true,
	"PKR": true, "PLN": true, "PYG": true, "QAR": true, "RON": true, "RSD": true,
	"RUB": true, "RWF": true, "SAR": true, "SBD": true, "SCR": true, "SDG": true,
	"SEK": true, "SGD": true, "SHP": true, "SLE": true, "SLL": true, "SOS": true,
	"SRD": true, "SSP": true, "STN": true, "SVC": true, "SYP": true, "SZL": true,
	"THB": true, "TJS": true, "TMT": true, "TND": true, "TOP": true, "TRY": true,
	"TTD": true, "TWD": true, "TZS": true, "UAH": true, "UGX": true, "USD": true,
	"USN": true, "UYI": true, "UYU": true, "UYW": true, "UZS": true, "VED": true,
	"VES": true, "VND": true, "VUV": true, "WST": true, "XAF": true, "XAG": true,
	"XAU": true, "XBA": true, "XBB": true, "XBC": true, "XBD": true, "XCD": true,
	"XCG": true, "XDR": true, "XOF": true, "XPD": true, "XPF": true, "XPT": true,
	"XSU": true, "XTS": true, "XUA": true, "XXX": true, "YER": true, "ZAR": true,
	"ZMW": true, "ZWG": true, "ZWL": true,
}

// commonRates holds hardcoded USD→target exchange rates for the most common
// currencies. Rates are approximate and static (not live). USD is the base.
var commonRates = map[string]float64{
	"USD": 1.0,
	"EUR": 0.92,
	"GBP": 0.79,
	"JPY": 157.0,
	"CNY": 7.25,
	"KRW": 1370.0,
	"TWD": 32.0,
	"HKD": 7.81,
	"INR": 83.5,
	"AUD": 1.51,
	"CAD": 1.36,
}

// currencySymbol holds the most common currency symbols.
var currencySymbol = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥",
	"KRW": "₩", "TWD": "NT$", "HKD": "HK$", "INR": "₹", "AUD": "A$",
	"CAD": "C$", "CHF": "CHF", "SEK": "kr", "SGD": "S$", "NZD": "NZ$",
	"MXN": "MX$", "BRL": "R$", "RUB": "₽", "ZAR": "R", "TRY": "₺",
	"THB": "฿", "PHP": "₱", "MYR": "RM", "IDR": "Rp", "VND": "₫",
	"PLN": "zł", "DKK": "kr", "NOK": "kr", "CZK": "Kč", "HUF": "Ft",
	"ILS": "₪", "UAH": "₴", "RON": "lei", "BGN": "лв", "HRK": "kn",
}

// IsValidCurrency reports whether code is a known ISO 4217 currency code.
func IsValidCurrency(code string) bool {
	return isoCurrencies[strings.ToUpper(code)]
}

// ListCurrencies returns all supported ISO 4217 currency codes, sorted.
func ListCurrencies() []string {
	out := make([]string, 0, len(isoCurrencies))
	for c := range isoCurrencies {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Rate returns the USD→code conversion rate. For currencies without a
// hardcoded rate, the rate is 1.0 (treated as USD-equivalent) and the
// second return value is false to indicate no rate is available.
func Rate(code string) (float64, bool) {
	r, ok := commonRates[strings.ToUpper(code)]
	if !ok {
		return 1.0, false
	}
	return r, true
}

// Symbol returns the display symbol for code, falling back to the code itself.
func Symbol(code string) string {
	if s, ok := currencySymbol[strings.ToUpper(code)]; ok {
		return s
	}
	return strings.ToUpper(code)
}

// Convert converts a USD amount to the target currency. When no rate is
// available, the amount is returned unchanged with a flag.
func Convert(usd float64, target string) (float64, bool) {
	target = strings.ToUpper(target)
	if target == "" || target == "USD" {
		return usd, true
	}
	r, ok := Rate(target)
	if !ok {
		return usd, false
	}
	return usd * r, true
}

// FormatMoney formats a USD amount in the target currency with its symbol.
// When no rate is available, it formats in USD with a note.
func FormatMoney(usd float64, target string) string {
	target = strings.ToUpper(target)
	if target == "" || target == "USD" {
		return fmt.Sprintf("$%.2f", usd)
	}
	conv, ok := Convert(usd, target)
	sym := Symbol(target)
	if !ok {
		return fmt.Sprintf("%s%.2f (no rate, USD)", sym, usd)
	}
	// JPY, KRW, VND, CLP, etc. are typically 0-decimal currencies.
	switch target {
	case "JPY", "KRW", "VND", "CLP", "PYG", "UGX", "RWF", "BIF", "DJF",
		"GNF", "KPW", "LAK", "SLL", "SOS", "TJS", "TND", "VUV", "XAF", "XOF", "XPF":
		return fmt.Sprintf("%s%.0f", sym, conv)
	default:
		return fmt.Sprintf("%s%.2f", sym, conv)
	}
}
