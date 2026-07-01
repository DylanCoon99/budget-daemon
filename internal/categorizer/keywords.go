package categorizer

import (
	"database/sql"
	"strings"
)

// KeywordMatcher is Tier 2 — a hardcoded map of keywords to categories.
// Free, instant, handles ~60-70% of typical consumer transactions.
type KeywordMatcher struct {
	db       *sql.DB
	rules    []keywordRule
	catCache map[string]int // category name -> id
}

type keywordRule struct {
	Keywords     []string
	CategoryName string
	MerchantName string // optional clean name
}

type KeywordResult struct {
	CategoryID   int
	CategoryName string
	MerchantName string
}

func NewKeywordMatcher(db *sql.DB) *KeywordMatcher {
	m := &KeywordMatcher{
		db:       db,
		catCache: make(map[string]int),
	}
	m.rules = defaultKeywordRules()
	return m
}

// Match checks the transaction description against keyword rules.
func (k *KeywordMatcher) Match(description string) (KeywordResult, bool) {
	descLower := strings.ToLower(description)

	for _, rule := range k.rules {
		for _, kw := range rule.Keywords {
			if strings.Contains(descLower, kw) {
				catID, err := k.categoryID(rule.CategoryName)
				if err != nil {
					continue
				}

				merchant := rule.MerchantName
				if merchant == "" {
					merchant = cleanMerchant(description)
				}

				return KeywordResult{
					CategoryID:   catID,
					CategoryName: rule.CategoryName,
					MerchantName: merchant,
				}, true
			}
		}
	}

	return KeywordResult{}, false
}

func (k *KeywordMatcher) categoryID(name string) (int, error) {
	if id, ok := k.catCache[name]; ok {
		return id, nil
	}

	var id int
	err := k.db.QueryRow(`SELECT id FROM categories WHERE name = ?`, name).Scan(&id)
	if err != nil {
		return 0, err
	}

	k.catCache[name] = id
	return id, nil
}

func defaultKeywordRules() []keywordRule {
	return []keywordRule{
		// Groceries
		{Keywords: []string{"wholefds", "whole foods"}, CategoryName: "Groceries", MerchantName: "Whole Foods"},
		{Keywords: []string{"walmart", "wal-mart"}, CategoryName: "Groceries", MerchantName: "Walmart"},
		{Keywords: []string{"kroger"}, CategoryName: "Groceries", MerchantName: "Kroger"},
		{Keywords: []string{"aldi"}, CategoryName: "Groceries", MerchantName: "Aldi"},
		{Keywords: []string{"trader joe"}, CategoryName: "Groceries", MerchantName: "Trader Joe's"},
		{Keywords: []string{"giant eagle"}, CategoryName: "Groceries", MerchantName: "Giant Eagle"},
		{Keywords: []string{"wegmans"}, CategoryName: "Groceries", MerchantName: "Wegmans"},
		{Keywords: []string{"target"}, CategoryName: "Shopping", MerchantName: "Target"},
		{Keywords: []string{"costco"}, CategoryName: "Groceries", MerchantName: "Costco"},
		{Keywords: []string{"sam's club", "samsclub"}, CategoryName: "Groceries", MerchantName: "Sam's Club"},

		// Dining Out
		{Keywords: []string{"mcdonald"}, CategoryName: "Dining Out", MerchantName: "McDonald's"},
		{Keywords: []string{"starbucks"}, CategoryName: "Dining Out", MerchantName: "Starbucks"},
		{Keywords: []string{"chipotle"}, CategoryName: "Dining Out", MerchantName: "Chipotle"},
		{Keywords: []string{"chick-fil-a", "chick fil a"}, CategoryName: "Dining Out", MerchantName: "Chick-fil-A"},
		{Keywords: []string{"taco bell"}, CategoryName: "Dining Out", MerchantName: "Taco Bell"},
		{Keywords: []string{"wendy"}, CategoryName: "Dining Out", MerchantName: "Wendy's"},
		{Keywords: []string{"burger king"}, CategoryName: "Dining Out", MerchantName: "Burger King"},
		{Keywords: []string{"subway"}, CategoryName: "Dining Out", MerchantName: "Subway"},
		{Keywords: []string{"dunkin"}, CategoryName: "Dining Out", MerchantName: "Dunkin'"},
		{Keywords: []string{"doordash"}, CategoryName: "Dining Out", MerchantName: "DoorDash"},
		{Keywords: []string{"uber eats", "ubereats"}, CategoryName: "Dining Out", MerchantName: "Uber Eats"},
		{Keywords: []string{"grubhub"}, CategoryName: "Dining Out", MerchantName: "Grubhub"},
		{Keywords: []string{"pizza hut"}, CategoryName: "Dining Out", MerchantName: "Pizza Hut"},
		{Keywords: []string{"domino"}, CategoryName: "Dining Out", MerchantName: "Domino's"},

		// Gas & Fuel
		{Keywords: []string{"shell oil", "shell service"}, CategoryName: "Gas & Fuel", MerchantName: "Shell"},
		{Keywords: []string{"exxon", "exxonmobil"}, CategoryName: "Gas & Fuel", MerchantName: "ExxonMobil"},
		{Keywords: []string{"bp ", "bp#"}, CategoryName: "Gas & Fuel", MerchantName: "BP"},
		{Keywords: []string{"sunoco"}, CategoryName: "Gas & Fuel", MerchantName: "Sunoco"},
		{Keywords: []string{"speedway"}, CategoryName: "Gas & Fuel", MerchantName: "Speedway"},
		{Keywords: []string{"sheetz"}, CategoryName: "Gas & Fuel", MerchantName: "Sheetz"},
		{Keywords: []string{"getgo", "get go"}, CategoryName: "Gas & Fuel", MerchantName: "GetGo"},
		{Keywords: []string{"kwik fill"}, CategoryName: "Gas & Fuel", MerchantName: "Kwik Fill"},
		{Keywords: []string{"country fair"}, CategoryName: "Gas & Fuel", MerchantName: "Country Fair"},

		// Utilities
		{Keywords: []string{"comcast"}, CategoryName: "Utilities", MerchantName: "Comcast"},
		{Keywords: []string{"verizon"}, CategoryName: "Utilities", MerchantName: "Verizon"},
		{Keywords: []string{"at&t", "att "}, CategoryName: "Utilities", MerchantName: "AT&T"},
		{Keywords: []string{"t-mobile", "tmobile"}, CategoryName: "Utilities", MerchantName: "T-Mobile"},
		{Keywords: []string{"spectrum"}, CategoryName: "Utilities", MerchantName: "Spectrum"},
		{Keywords: []string{"penelec", "firstenergy"}, CategoryName: "Utilities", MerchantName: "Penelec"},
		{Keywords: []string{"national fuel"}, CategoryName: "Utilities", MerchantName: "National Fuel"},
		{Keywords: []string{"erie water"}, CategoryName: "Utilities", MerchantName: "Erie Water Works"},

		// Subscriptions
		{Keywords: []string{"netflix"}, CategoryName: "Subscriptions", MerchantName: "Netflix"},
		{Keywords: []string{"spotify"}, CategoryName: "Subscriptions", MerchantName: "Spotify"},
		{Keywords: []string{"hulu"}, CategoryName: "Subscriptions", MerchantName: "Hulu"},
		{Keywords: []string{"disney+"}, CategoryName: "Subscriptions", MerchantName: "Disney+"},
		{Keywords: []string{"apple.com/bill"}, CategoryName: "Subscriptions", MerchantName: "Apple"},
		{Keywords: []string{"youtube premium", "google *youtube"}, CategoryName: "Subscriptions", MerchantName: "YouTube Premium"},
		{Keywords: []string{"amazon prime"}, CategoryName: "Subscriptions", MerchantName: "Amazon Prime"},
		{Keywords: []string{"chatgpt", "openai"}, CategoryName: "Subscriptions", MerchantName: "OpenAI"},

		// Shopping
		{Keywords: []string{"amazon", "amzn"}, CategoryName: "Shopping", MerchantName: "Amazon"},
		{Keywords: []string{"best buy"}, CategoryName: "Shopping", MerchantName: "Best Buy"},
		{Keywords: []string{"home depot"}, CategoryName: "Shopping", MerchantName: "Home Depot"},
		{Keywords: []string{"lowes", "lowe's"}, CategoryName: "Shopping", MerchantName: "Lowe's"},

		// Transportation
		{Keywords: []string{"uber trip", "uber* trip"}, CategoryName: "Transportation", MerchantName: "Uber"},
		{Keywords: []string{"lyft"}, CategoryName: "Transportation", MerchantName: "Lyft"},
		{Keywords: []string{"parking"}, CategoryName: "Transportation"},

		// Healthcare
		{Keywords: []string{"cvs"}, CategoryName: "Healthcare", MerchantName: "CVS"},
		{Keywords: []string{"walgreens"}, CategoryName: "Healthcare", MerchantName: "Walgreens"},
		{Keywords: []string{"rite aid"}, CategoryName: "Healthcare", MerchantName: "Rite Aid"},

		// Fees & Interest
		{Keywords: []string{"interest charge"}, CategoryName: "Fees & Interest"},
		{Keywords: []string{"late fee"}, CategoryName: "Fees & Interest"},
		{Keywords: []string{"annual fee"}, CategoryName: "Fees & Interest"},
		{Keywords: []string{"overdraft"}, CategoryName: "Fees & Interest"},

		// ATM/Cash
		{Keywords: []string{"atm withdrawal", "atm w/d"}, CategoryName: "ATM/Cash"},
		{Keywords: []string{"cash advance"}, CategoryName: "ATM/Cash"},

		// Transfers
		{Keywords: []string{"transfer from", "transfer to"}, CategoryName: "Transfer"},
		{Keywords: []string{"online transfer"}, CategoryName: "Transfer"},
		{Keywords: []string{"zelle"}, CategoryName: "Transfer", MerchantName: "Zelle"},
		{Keywords: []string{"venmo"}, CategoryName: "Transfer", MerchantName: "Venmo"},
		{Keywords: []string{"paypal"}, CategoryName: "Transfer", MerchantName: "PayPal"},

		// Income
		{Keywords: []string{"direct deposit", "payroll"}, CategoryName: "Income"},
		{Keywords: []string{"iron mountain"}, CategoryName: "Income", MerchantName: "Iron Mountain"},

		// Insurance
		{Keywords: []string{"geico"}, CategoryName: "Insurance", MerchantName: "GEICO"},
		{Keywords: []string{"state farm"}, CategoryName: "Insurance", MerchantName: "State Farm"},
		{Keywords: []string{"progressive"}, CategoryName: "Insurance", MerchantName: "Progressive"},
		{Keywords: []string{"allstate"}, CategoryName: "Insurance", MerchantName: "Allstate"},
	}
}
