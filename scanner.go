package main

import (
	"context"
	"fmt"
	"log"
    "math"
	"strings"
	"sync/atomic"
	"time"
)

type Scanner struct {
	cfg      *Config
	cl       *Client
    state    *JS8State    // track state of js8call host
	lastAct  atomic.Int64 // unix nano of last RX activity
	curLabel atomic.Value // string
	lastSent map[string]time.Time
}

type JS8State struct {
    TXActive        bool
    QSOActive       bool        // are we directing TXs to someone, or have we heard someone talking directly to us
    QSOTarget       string      // who, if anyone, are directing TXs to
    QSOFreq         int64       // actual frequency (ie including offset) used by someone talking to us
    PTT             string      // 'off' or 'on'
    LastTX          time.Time
    LastQSOExchange time.Time
}

func NewScanner(cfg *Config, cl *Client, state *JS8State) *Scanner {
	s := &Scanner{cfg: cfg, cl: cl, state: state, lastSent: map[string]time.Time{}}
	s.curLabel.Store("?")
	return s
}

func (s *Scanner) label() string { return s.curLabel.Load().(string) }

func (s *Scanner) noteActivity() { s.lastAct.Store(time.Now().UnixNano()) }

// HandleEvents prints and tracks everything arriving from the API.
func (s *Scanner) HandleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-s.cl.Events:
			if !ok {
				log.Println("API connection closed")
				return
			}
			switch m.Type {
			case "RX.DIRECTED":
				s.noteActivity()
                // standard protocol used by hams is intended recipient then sender then the message
				fmt.Printf("[%s] DIRECTED %s %s snr=%d  %s\n", s.label(), m.Str("TO"), m.Str("FROM"), m.Int("SNR"), m.Value)
				log.Printf("[%s] DIRECTED %s %s snr=%d  %s\n", s.label(), m.Str("TO"), m.Str("FROM"), m.Int("SNR"), m.Value)
                if m.Str("TO") == s.cfg.Callsign {
                    s.state.QSOActive = true
                    s.state.QSOFreq = m.Int("FREQ")
                    s.state.LastQSOExchange = time.Now()
                }
			case "RX.ACTIVITY":
				s.noteActivity()
				fmt.Printf("[%s] ACTIVITY off=%d snr=%d  %s\n", s.label(), m.Int("OFFSET"), m.Int("SNR"), m.Value)
				log.Printf("[%s] ACTIVITY off=%d snr=%d  %s\n", s.label(), m.Int("OFFSET"), m.Int("SNR"), m.Value)
			case "RIG.FREQ":
				fmt.Printf("rig dial=%d offset=%d", m.Int("DIAL"), m.Int("OFFSET"))
				log.Printf("rig dial=%d offset=%d", m.Int("DIAL"), m.Int("OFFSET"))
                // hearing this means we changed frequencies, so QSO is no longer active
                s.state.QSOActive = false
                s.state.QSOFreq = 0
			case "STATION.STATUS":
				log.Printf("station reported dial=%d offset=%d", m.Int("DIAL"), m.Int("OFFSET"))
                if s.cfg.TXEnabled == true {
				    log.Printf("\tTX speed mode=%v\n", m.Int("SPEED"))
                }
                s.state.QSOTarget = m.Str("SELECTED")
                if  s.state.QSOTarget != "" {
				    log.Printf("\tSTATION IS SET TO QSO WITH %v\n",s.state.QSOTarget)
                    s.state.QSOActive = true
                    s.state.LastQSOExchange = time.Now()
                } else {
                    s.state.QSOActive = false
                }
			case "RX.SPOT":
                // {RX.SPOT  map[CALL:KB8JEP DIAL:7.078e+06 FREQ:7.078854e+06 GRID: OFFSET:854 SNR:-18 _ID:-1]}
                log.Printf("HEARD %v with SNR %v  at %d Hz, offset of %d\n", m.Str("CALL"), m.Int("SNR"), m.Int("DIAL"), m.Int("OFFSET"))
            case "RIG.PTT":
                //{RIG.PTT on map[PTT:true UTC:1.793995274801e+12 _ID:-1]}
                s.state.PTT = m.Value
                if s.state.PTT == "off" {
                    s.state.LastTX = time.Now()
                    if s.state.QSOActive {
                        s.state.LastQSOExchange = time.Now()
                    }
                }
                log.Printf("TX state has changed to [%v]\n", m.Value)
            case "TX.FRAME":
                log.Printf("JS8Call sent a data frame\n")

            default:
				log.Printf("Unhandled return from API: %v\n%v", m.Type, m)
			}
		}
	}
}

// HandleLogs prints lines from ALL.TXT / DIRECTED.TXT.
func (s *Scanner) HandleLogs(ctx context.Context, lines <-chan LogLine) {
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-lines:
			s.noteActivity()
			//fmt.Printf("[%s] log:%s  %s\n", s.label(), l.Source, l.Text)
			log.Printf("[%s] log:%s  %s\n", s.label(), l.Source, l.Text)
		}
	}
}

// Run cycles through the configured frequencies until ctx is cancelled.
//   when starting identify what freq/band the rig is already set to and if it's in our list start from there
//   if it's not, determine the closest freq that is and start there
func (s *Scanner) Run(ctx context.Context) {

    // use the API to ask what the freq currently is, wait half a second for the API response before checking the value.
    // allow a max of 15 seconds to determine the freq, anything longer than this and things are just too slow overall
    for i := 0; i < 30; i++ {
        if err := s.cl.GetFreq(); err != nil {
            log.Printf("Unable to query rig for initial frequency: %v", err)
            return
        }
        time.Sleep(500*time.Millisecond)
        if s.cl.dial.Load() != 0 {
            log.Printf("Rig frequency at start is: %v\n", s.cl.dial.Load())
            break
        }
    }

    // start the scanning from the closest desired frequency to where js8call is already set to
    dev := 144e6    // start with a very large deviation value to ensure we narrow down to the closest freq
    startIndex := 0
	for i := 0; i < len(s.cfg.Freqs) ; i++ {
        if s.cl.dial.Load() == s.cfg.Freqs[i].Hz {
            startIndex = i
            break   // already on a desired frequency start there
        }
        if math.Abs(float64(s.cl.dial.Load() - s.cfg.Freqs[i].Hz)) < dev {
            dev = math.Abs(float64(s.cl.dial.Load() - s.cfg.Freqs[i].Hz))
            startIndex = i  // closest match so far
        }
    }

    log.Printf("Start scanning at freq %d\n", s.cl.dial.Load())
    // I think there may be a bug in JS8Call v3.0.3 where sending certain API requests while the rig is transmitting causes a seg fault
    // most likely is if asking to qsy during tx
    //  however even just asking for the current frequency results in this obvious issue every time:
    //
    //       26-11-06T18:37:01.652Z: QObject: Cannot create children for a parent that is in a different thread.
    //       (Parent is QNativeSocketEngine(0x7fba14050620), parent's thread is QThread(0x7ffd57ba1768), current thread is QThread(0x55b9ee2e6e00)

	for i := startIndex; ; i = (i + 1) % len(s.cfg.Freqs) {
		f := s.cfg.Freqs[i]
		if !s.dwell(ctx, f) {
			return
		}
        // we're done on this frequency, any QSOs are no longer active 
        s.state.QSOActive = false
        s.state.QSOFreq = 0
        s.state.QSOTarget = ""
	}
}

func (s *Scanner) dwell(ctx context.Context, f Freq) bool {
	log.Printf("QSY -> %s (%.3f kHz)", f.Label, float64(f.Hz)/1000)
	if err := s.cl.SetFreq(f.Hz, s.cfg.Offset); err != nil {
		log.Printf("set freq failed: %v", err)
	}
	s.curLabel.Store(f.Label)
	start := time.Now()
	s.lastAct.Store(0)

	// Give the rig a moment to settle before any transmit.
	if !sleep(ctx, 3*time.Second) {
		return false
	}

    // I think we shouldn't call this function (sends heartbeat, snr check, etc)
    //   unless TX is confirmed enabled here, even if the called function is checking it
    //   IE - suboptimal to even call the function if we're just going to return MOST of the time
    //   default behavior should be listen only since TX requires a station operator be present
	s.maybeSend(f)

	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
        // TODO - if we're in an active QSO we don't care how long we spend on this frequency
        //        if neither side says anything for 20 minutes we'll consider the QSO as over
        if s.state.QSOActive {
            if time.Since(s.state.LastQSOExchange) < time.Duration(20*time.Minute) {
                continue
            }
            log.Printf("QSO with %v appears to no longer be active\n", s.state.QSOTarget)
        }
		el := time.Since(start)
		if el >= s.cfg.MaxDwell {
			return true
		}
		if el >= s.cfg.MinDwell {
			la := s.lastAct.Load()
			if la == 0 || time.Since(time.Unix(0, la)) > s.cfg.QuietTime {
				return true
			}
		}
	}
}

func (s *Scanner) maybeSend(f Freq) {
	if !s.cfg.TXEnabled || !s.cfg.SendOn[strings.ToLower(f.Label)] {
		return
	}
	for _, name := range s.cfg.SendList {
		key := f.Label + "/" + name
		if t, ok := s.lastSent[key]; ok && time.Since(t) < s.cfg.MinInterval {
			continue
		}
		text := s.cfg.Messages[name]
		log.Printf("TX [%s] %s", f.Label, text)
		if err := s.cl.SendMessage(text); err != nil {
			log.Printf("send failed: %v", err)
			continue
		}
		s.lastSent[key] = time.Now()
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
