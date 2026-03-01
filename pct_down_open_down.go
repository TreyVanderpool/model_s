package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	odb "github.com/TreyVanderpool/oliver-golib/db"
	oinit "github.com/TreyVanderpool/oliver-golib/init"
	ol "github.com/TreyVanderpool/oliver-golib/logging"
	osql "github.com/TreyVanderpool/oliver-golib/sql"
	ou "github.com/TreyVanderpool/oliver-golib/utils"

	"github.com/ziutek/mymysql/mysql"
	_ "github.com/ziutek/mymysql/thrsafe"
)

const (
)

var (
  Log               ol.ILogger
  DB                *odb.DB
  SQLs              osql.SQLs
  gsStartingDate    *string = flag.String( "sd", time.Now().Format( ou.YYYY_MM_DD ), "starting date" )
  // giNegDays         *int = flag.Int( "nd", 3, "number of negative days to look at" )
  gfPctChgParm      *float64 = flag.Float64( "pc", 2, "percent change to check" )
  gfLossPct         *float64 = flag.Float64( "lp", 2.0, "loss percent" )
  giPlaysPerDay     *int = flag.Int( "ppd", 10, "plays per day" )
  gfBank            *float64 = flag.Float64( "bank", 10000, "starting bank simulation" )
  gfOpenDownPct     *float64 = flag.Float64( "odp", 2, "open down percent" )
  gfExitPercents    []float64
  gcHitDates        map[string][]_hit = make( map[string][]_hit )

  gfTotalPct        float64 = 0.0
  giDaysPlayed      int = 0
)

type _hit struct {
  Symbol        string
  Values        []osql.OCDate
}

func main() {
  lsDBName := flag.String( "db", "stocks_test", "database name" )
  lsSymbol := flag.String( "s", "", "single symbol to look at" )
  lsSymbolListName := flag.String( "sln", "top_list_20241205", "symbol list name" )
  lsExcludeSymbols := flag.String( "exclude", "model_r_exclude", "exclude symbol list" )
  lsLvl := flag.String( "lvl", "info", "log level" )
  lsExitPercents := flag.String( "ep", "2.0", "exit percent")
  lsNegDays := flag.String( "nd", "3", "number of negative days to look at" )
  flag.Parse()

  if *gfPctChgParm > 0 { *gfPctChgParm *= -1 }
  if *gfLossPct > 0 { *gfLossPct *= -1 }
  gfExitPercents = make( []float64, *giPlaysPerDay )
  lsValues := strings.Split( *lsExitPercents, "," )
  liNegDays := make( []int, 0 )

  // Go through all the exit percents and set them up
  // based on the number of plays per day.
  for i, lV := range lsValues {
    lfValue, _ := strconv.ParseFloat( strings.Trim( lV, " " ), 64 )
    gfExitPercents[i] = ou.TTF( lfValue > 0, lfValue * -1, lfValue ).(float64)
  }
  for i := len( lsValues ); i < len( gfExitPercents ); i++ {
    gfExitPercents[i] = gfExitPercents[len(lsValues)-1]
  }

  lsValues = strings.Split( *lsNegDays, "," )
  
  for _, lV := range lsValues {
    liNbr, _ := strconv.Atoi( lV )
    liNegDays = append( liNegDays, liNbr )
  }

  Log = oinit.Init( oinit.INIT_LOG, *lsLvl ).(ol.ILogger)
  DB = oinit.Init( oinit.INIT_DB, *lsDBName, Log ).(*odb.DB)
  SQLs = oinit.Init( oinit.INIT_SQLS, DB, Log ).(osql.SQLs)

  Log.SetPatterns( "%M\n", "%D %-5L %T:%F:%# %M\n" )
  // Get symbol list to test
  lsSymList, _ := _InitGetSymbols( *lsSymbol, *lsSymbolListName, *lsExcludeSymbols )

  Log.Info( "Starting: Symbol List: %4d", len( lsSymList ) )

  // Log.Info( "Symbols to process: %d", len( lsSymList ) )

  _LoadProcessingMap( lsSymList, liNegDays )
  // _EvaluateDatesOpenDown()
  _EvaluateDatesOpenDownAndDownLP()

  Log.Info( "Total Days Played: %d  Avg Pct: %5.2f", giDaysPlayed, gfTotalPct / float64(giDaysPlayed) )
}

//------------------------------------------------------------
// Function: _InitGetSymbols
//------------------------------------------------------------
func _InitGetSymbols( asSymbol, asSymbolListName, asExcludeSymbolList string ) ( []string, map[string]string ) {

  lsSymList := make( []string, 0 )
  lcSymMap := make( map[string]string )
  // lsExcludeList := make( []string, 0 )

  if asSymbol > "" {
    lsSymList = append( lsSymList, asSymbol )
    lcSymMap[asSymbol] = asSymbol
  } else {
    var err    error
    lsSymList, lcSymMap, err = SQLs.S_SymbolsToProcessName( asSymbolListName )
    if err != nil {
      return lsSymList, lcSymMap
    }
  }

  // if asExcludeSymbolList > "" {
  //   lsExcludeList, _, _ = SQLs.S_SymbolsToProcessName( asExcludeSymbolList )
  // }

  // lsSymList, lcSymMap = _RemoveDowJonesSymbol( lcSymMap, lsExcludeList )

  return lsSymList, lcSymMap
}

//------------------------------------------------------------
// Function: _InitGetSymbols
//------------------------------------------------------------
func _LoadProcessingMap( asSymbols []string, aiNegDays []int ) ( error ) {
  lcResult, err := _QueryOpenClose( asSymbols )

  if err != nil {
    Log.Exception( err )
    return err
  }

  lcRow := lcResult.MakeRow()
  lsHoldSym := ""
  lcValues := make( []osql.OCDate, 0 )

  for {
    err := lcResult.ScanRow( lcRow )
    if err == io.EOF {
      break
    }
    if err != nil {
      Log.Exception( err )
      return err
    }
    lsSymbol := lcRow.Str( 1 )
    if lsHoldSym == "" { lsHoldSym = lsSymbol }
    if lsHoldSym == lsSymbol {
      lNew := osql.OCDate{ Date: lcRow.Str( 0 ),
                           Open: lcRow.Float( 2 ),
                           Close: lcRow.Float( 3 ),
                           Low: lcRow.Float( 4 ),
                           High: lcRow.Float( 5 ) }
      lcValues = append( lcValues, lNew )
    } else {
      for _, lND := range aiNegDays {
        _TestValues( lsHoldSym, lND, lcValues )
      }
      lsHoldSym = lsSymbol
      lcValues = make( []osql.OCDate, 0 )
    }
  }

  return nil
}

//------------------------------------------------------------
// Function: _QueryOpenClose
//------------------------------------------------------------
func _QueryOpenClose( asSymbols []string ) ( mysql.Result, error ) {
  lsSymbols := ""

  for _, s := range asSymbols {
    if s == "$DJI" { continue }
    if lsSymbols > "" {
      lsSymbols += ","
    }
    lsSymbols += "'" + s + "'"
  }

  lsSQL := fmt.Sprintf( "select tran_date, " +
                        "       symbol, " + 
                        "       open_value, " +
                        "       close_value, " +
                        "       low_value, " +
                        "       high_value " +
                        "from   open_close " +
                        "where  symbol in ( %s ) " +
                        "and    tran_date >= '%s' " +
                        "order by symbol, tran_date", 
                        lsSymbols, *gsStartingDate )
                      
  lcResult, err := DB.Conn.Start( lsSQL )
  return lcResult, err
}

//------------------------------------------------------------
// Function: _TestValues
//------------------------------------------------------------
func _TestValues( asSymbol string, aiNegDays int, acValues []osql.OCDate ) {
  liDaysNeg := 0

  for i := aiNegDays + 1; i < len( acValues ); i++ {
    liDaysNeg = 0
    // Check if the current day opens below the previous day's close
    lfOpenPct := ou.PctChg( acValues[i-1].Close, acValues[i].Open )
    // if acValues[i].Open < acValues[i-1].Close && lfOpenPct < -1.0 {
    if lfOpenPct <= *gfOpenDownPct {
      for j := i - 1; j >= i - aiNegDays; j-- {
        if acValues[j-1].Close > acValues[j].Close {
          lfPct := ou.PctChg( acValues[j-1].Close, acValues[j].Close )
          // Is the percent change below the parm value
          if lfPct < *gfPctChgParm {
            liDaysNeg++
          }
        } else {
          break
        }
      }
      if liDaysNeg == aiNegDays {
        _PrintTestResult( asSymbol, aiNegDays, acValues, i )
        _AddHit( asSymbol, aiNegDays, acValues, i )
      }
    }
  }
}

//------------------------------------------------------------
// Function: _PrintTestResult
//------------------------------------------------------------
func _PrintTestResult( asSymbol string, aiNegDays int, acValues []osql.OCDate, aiIndex int ) {
  lsText := acValues[aiIndex-aiNegDays].Date + " "

  for i := aiIndex - aiNegDays; i < aiIndex; i++ {
    lsText += fmt.Sprintf( "%7.2fc ", acValues[i].Close )
  }

  lsText += fmt.Sprintf( "%7.2fo", acValues[aiIndex].Open )

  Log.Debug( "%-6s: %d negative days: %s", asSymbol, aiNegDays, lsText )
}

//------------------------------------------------------------
// Function: _AddHit
//------------------------------------------------------------
func _AddHit( asSymbol string, aiNegDays int, acValues []osql.OCDate, aiIndex int ) {
  lcHit := _hit{ Symbol: asSymbol, Values: acValues[aiIndex-aiNegDays : aiIndex+1] }

  lcHits, lbFnd := gcHitDates[acValues[aiIndex].Date]

  if ! lbFnd {
    lcHits = make( []_hit, 0 )
  }

  lcHits = append( lcHits, lcHit )
  gcHitDates[acValues[aiIndex].Date] = lcHits
}

//------------------------------------------------------------
// Function: _EvaluateDatesOpenDown
//------------------------------------------------------------
func _EvaluateDatesOpenDown() {
  Log.Info( "Total Date Entries: %d", len( gcHitDates ) )

  lcDate, _ := time.Parse( ou.YYYY_MM_DD, *gsStartingDate )
  lsCurrDate := time.Now().Format( ou.YYYY_MM_DD )

  for{
    lsDate := lcDate.Format( ou.YYYY_MM_DD )
    if lsDate >= lsCurrDate { break }

    if lcHits, lbFnd := gcHitDates[lsDate]; lbFnd {
      lsEndPct := ""
      lfTotPct := 0.0
      for _, lHit := range lcHits {
        liLen := len( lHit.Values )
        lcLast := lHit.Values[liLen-1]
        lfLowPct := ou.PctChg( lcLast.Open, lcLast.Low )
        lfClosePct := ou.PctChg( lcLast.Open, lcLast.Close )
        lfEndPct := lfClosePct
        lsEndPct = "c"
        if lfLowPct < *gfLossPct { 
          lfLowPct = *gfLossPct
          lsEndPct = "l"
          lfEndPct = lfLowPct
        }
        lfTotPct += lfEndPct
        Log.Info( "HIT1D:  -> %s %-6s  %7.2fo  %7.2fc  %7.2fl  %6.2fl%%  %6.2fc%%  %6.2fe%s%%",
                  lsDate,
                  lHit.Symbol,
                  lcLast.Open,
                  lcLast.Close,
                  lcLast.Low,
                  lfLowPct,
                  lfClosePct,
                  lfEndPct,
                  lsEndPct )
      }
      Log.Info( "HIT1T: %s : Hits: %2d  Day Pct: %5.2f",
                lsDate, len( lcHits ), lfTotPct / float64(len( lcHits )) )
    }

    lcDate = lcDate.AddDate( 0, 0, 1 )
  }
}

//------------------------------------------------------------
// Function: _EvaluateDatesOpenDownAndDownLP
//------------------------------------------------------------
func _EvaluateDatesOpenDownAndDownLP() {
  // Log.Info( "Total Date Entries: %d", len( gcHitDates ) )

  lcDate, _ := time.Parse( ou.YYYY_MM_DD, *gsStartingDate )
  lsCurrDate := time.Now().Format( ou.YYYY_MM_DD )

  for{
    lsDate := lcDate.Format( ou.YYYY_MM_DD )
    if lsDate >= lsCurrDate { break }
    lcHits, lbFnd := gcHitDates[lsDate]
    if ! lbFnd { 
      lcDate = lcDate.AddDate( 0, 0, 1 )
      continue
    }

    lcHitsMap := make( map[string]_hit )
    for i, lHit := range lcHits {
      lcHitsMap[lHit.Symbol] = lcHits[i]
    }

    lfTotPct := 0.0
    liHits := 0
    lcHits = make( []_hit, 0 )

    // Find the number that hits the condition
    // Using the map it acts like a random selection
    for _, lHit := range lcHitsMap {
      liLen := len( lHit.Values )
      lcLast := lHit.Values[liLen-1]
      lfLowPct := ou.PctChg( lcLast.Open, lcLast.Low )
      if lfLowPct < *gfLossPct {
        liHits++
        lcHits = append( lcHits, lcHitsMap[lHit.Symbol] )
        if liHits >= *giPlaysPerDay { break }
      }
    }

    for _, lHit := range lcHits {
      liLen := len( lHit.Values )
      lcLast := lHit.Values[liLen-1]
      lfLowPct := ou.PctChg( lcLast.Open, lcLast.Low )
      lfBuyValue := lcLast.Open + (lcLast.Open * (*gfLossPct/100))
      lfLowPct = ou.PctChg( lfBuyValue, lcLast.Low )
      lfClosePct := ou.PctChg( lfBuyValue, lcLast.Close )
      lfExitPct := lfClosePct
      lsExitPct := "c"
      if lfLowPct <= gfExitPercents[liHits-1] {
        lfExitPct = gfExitPercents[liHits-1]
        lsExitPct = "l"
      }
      lfTotPct += lfExitPct
      Log.Info( "HIT2D:  -> %s %-6s  %7.2fo  %7.2fl  %6.2fb  %7.2fc  %6.2fe%s%%",
                lsDate,
                lHit.Symbol,
                lcLast.Open,
                lcLast.Low,
                lfBuyValue,
                lcLast.Close,
                lfExitPct,
                lsExitPct )
    }

    if liHits > 0 {
      lfDayPct := lfTotPct / float64(liHits)
      *gfBank += *gfBank * (lfDayPct/100)
      Log.Info( "HIT2T: %s : Hits: %2d  Day Pct: %6.2f  Bank: %8s",
                lsDate, liHits, lfDayPct,
                ou.Commas( "%.0f", *gfBank ) )
      giDaysPlayed++
      gfTotalPct += lfTotPct
    }

    lcDate = lcDate.AddDate( 0, 0, 1 )
  }
}