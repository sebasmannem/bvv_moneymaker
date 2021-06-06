package internal

import (
	"fmt"
	"github.com/sebasmannem/bvvmoneymaker/pkg/moving_average"
	"github.com/shopspring/decimal"
)

type BvvMarketTrend int

type BvvMarkets map[string]*BvvMarket

type MarketNotInConfigError struct {
	error
}

func newMarketNotInConfigError(marketName string) MarketNotInConfigError {
	return MarketNotInConfigError{
		fmt.Errorf("skipping market %s, not in config", marketName),
	}
}

type BvvMarket struct {
	From       string `yaml:"symbol"`
	To         string `yaml:"fiat"`
	handler    *BvvHandler
	config     bvvMarketConfig
	inverse    *BvvMarket
	Available  decimal.Decimal `yaml:"available"`
	InOrder    decimal.Decimal `yaml:"inOrder"`
	Price      decimal.Decimal `yaml:"price"`
	Min        decimal.Decimal `yaml:"min"`
	Max        decimal.Decimal `yaml:"max"`
	rh         *RibbonHandler
}

func NewBvvMarket(bh *BvvHandler, symbol string, fiatSymbol, available string, inOrder string) (market BvvMarket,
	err error) {
	config, found := bh.config.Markets[symbol]
	if !found {
		return market, newMarketNotInConfigError(symbol)
	}

	decMin, err := decimal.NewFromString(config.MinLevel)
	if err != nil {
		decMin = decimal.Zero
	} else if decMin.LessThan(decimal.Zero) {
		decMin = decimal.Zero
	}
	decMax, err := decimal.NewFromString(config.MaxLevel)
	if err != nil {
		decMax = decimal.Zero
	} else if decMax.LessThan(decMin) {
		decMax = decimal.Zero
	}
	decAvailable, err := decimal.NewFromString(available)
	if err != nil {
		return market, fmt.Errorf("could not convert available to Decimal %s: %e", available, err)
	}
	decInOrder, err := decimal.NewFromString(inOrder)
	if err != nil {
		return market, fmt.Errorf("could not convert inOrder to Decimal %s: %e", inOrder, err)
	}
	market = BvvMarket{
		From:      symbol,
		To:        fiatSymbol,
		handler:   bh,
		config:    config,
		Available: decAvailable,
		InOrder:   decInOrder,
	}
	if config.RibbonConfig.Enabled() {
		market.rh, err = NewRibbonHandler(&market, config.RibbonConfig)
		if err != nil {
			return BvvMarket{}, nil
		}
	}
	err = market.setPrice(bh.prices)
	if err != nil {
		return BvvMarket{}, err
	}
	market.inverse, err = market.reverse()
	market.inverse.inverse = &market

	if decMin.Equal(decimal.Zero) || decMax.Equal(decimal.Zero) {
		fmt.Printf("Disabling Min/Max for %s\n", market.Name())
		market.inverse.Max = decimal.Zero
		market.inverse.Min = decimal.Zero
		market.Max = decimal.Zero
		market.Min = decimal.Zero
	} else {
		// Because Max and Min are in EUR, not in Crypto, we set them in inverse and calculate for market from inverse
		market.inverse.Max = decMax
		market.inverse.Min = decMin
		market.Max = market.inverse.exchange(decMax)
		market.Min = market.inverse.exchange(decMin)
	}
	if err != nil {
		return BvvMarket{}, err
	}
	bh.markets[market.Name()] = &market
	bh.markets[market.inverse.Name()] = market.inverse
	return market, nil
}

func (bm BvvMarket) reverse() (reverse *BvvMarket, err error) {
	if bm.Price.Equal(decimal.NewFromInt32(0)) {
		return &BvvMarket{}, fmt.Errorf("cannot create a reverse when the prise is 0")
	}
	reverse = &BvvMarket{
		From:      bm.To,
		To:        bm.From,
		Price:     decimal.NewFromInt32(1).Div(bm.Price),
		Available: bm.exchange(bm.Available),
		InOrder:   bm.exchange(bm.InOrder),
	}
	return reverse, nil
}

func (bm BvvMarket) exchange(amount decimal.Decimal) (balance decimal.Decimal) {
	return bm.Price.Mul(amount)
}

func (bm BvvMarket) GetExpectedRate() (total decimal.Decimal, err error) {
	if bm.rh == nil || bm.rh.emas == nil {
		return decimal.Zero, fmt.Errorf("cannot get expected rate without RibbonHandler")
	}
	sum := decimal.Zero
	for _, ema := range bm.rh.emas {
		emaval, err := ema.GetWithOffset()
		if err != nil {
			return total, err
		}
		sum = sum.Add(emaval)
	}
	return sum.Div(decimal.NewFromInt(int64(len(bm.rh.emas)))), nil
}

func (bm BvvMarket) GetBandWidth(ema int) (bw moving_average.MABandwidth, err error) {
	if bm.rh == nil  {
		return moving_average.MABandwidth{}, fmt.Errorf("cannot get bandwidth without RibbonHandler")
	} else if len(bm.rh.emas) <= ema {
		return moving_average.MABandwidth{}, fmt.Errorf("Ribbon %d not defined", ema)
	}
	return bm.rh.emas[ema].GetBandwidth()
}

func (bm BvvMarket) Trend() (trend moving_average.Trend, err error) {
	if bm.rh == nil || len(bm.rh.emas) < 2 {
		return moving_average.UndefinedTrend, fmt.Errorf("to detect the trend we need a Ribbon with at least 2 ema's")
	}
	for i, ema := range bm.rh.emas {
		if i == 0 {
			// First cannot be compared with previous
			continue
		}
		_, emaTrend, err := ema.Compare(bm.rh.emas[i-1])
		if err != nil {
			return moving_average.UndefinedTrend, err
		}
		if i == 1 {
			// Second will be compared with first, and used as base value
			trend = emaTrend
		}
		if i>1 && emaTrend != trend {
			return moving_average.IndecisiveTrend, nil
		}
	}
	return
}

func (bm BvvMarket) Total() (total decimal.Decimal) {
	return bm.Available.Add(bm.InOrder)
}

func (bm BvvMarket) Name() (name string) {
	return fmt.Sprintf("%s-%s", bm.From, bm.To)
}

func (bm *BvvMarket) setPrice(prices map[string]decimal.Decimal) (err error) {
	var found bool
	bm.Price, found = prices[bm.Name()]
	if !found {
		return fmt.Errorf("could not find price for market %s", bm.Name())
	}
	// With this, pretty-print will also print this one
	//bm.FiatAvailable = fmt.Sprintf("%s %s", bm.FiatSymbol, bm.MarketFiatCurrency())
	return nil
}
