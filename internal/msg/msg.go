// Package msg holds every piece of text the bot sends, so wording is edited in one place and nowhere else.
//
//   - Fan-facing text is Indonesian (decided with the fancord head).
//   - Ops text is English and only ever goes to the private dev/ops channel.
package msg

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// markdownChars are the characters Discord reads as formatting, plus the ones that start a mention or a link. A backslash in front of any of them makes Discord show the character itself.
const markdownChars = "\\*_~|`<>[]@"

// escape makes text from outside (a YouTube title, say) safe to put inside a message: shown exactly as written, never as formatting. Without it, a title such as "Part 1 || Part 2 || Part 3" would turn into a hidden spoiler.
// Every title that goes into a message must pass through here, so any text that includes one is built in this file.
func escape(s string) string {
	s = strings.Join(strings.Fields(s), " ") // one line, single spaces
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(markdownChars, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---- Fan-facing (Indonesian) -------------------------------------------

const (
	PingDescription = "Cek apakah bot masih hidup"
	UnknownCommand  = "Perintah ini belum dikenal."
	CommandFailed   = "Maaf, ada yang salah. Coba lagi sebentar lagi."

	// Dev-only command texts.
	DevOnly                   = "Perintah ini hanya bisa dipakai di channel dev."
	DevStreamDescription      = "Simulasikan stream palsu (khusus dev)"
	DevStreamStateDescription = "Status stream yang ingin disimulasikan"

	DevCheckDescription        = "Cek stream terbaru di YouTube tanpa mengumumkan (khusus dev)"
	DevCheckChannelDescription = "ID channel YouTube (UC...); kosong = channel Yuura"
	DevCheckPagesDescription   = "Jumlah halaman yang dicek, 50 video per halaman (1-5, bawaan 1)"
	DevCheckNoKey              = "YouTube belum diatur. Isi YOUTUBE_API_KEY di .env lalu jalankan ulang bot."
	DevCheckNoChannel          = "Belum ada channel. Isi YOUTUBE_CHANNEL_ID di .env atau isi opsi channel."
)

// StreamUpcoming announces a scheduled stream. The start time is shown twice: as a full date and as "in 2 hours", both rendered by Discord for each viewer.
func StreamUpcoming(title, url string, start time.Time) string {
	title = escape(title)
	if start.IsZero() {
		return fmt.Sprintf("⏰ **Jadwal stream**: %s\n%s", title, url)
	}
	return fmt.Sprintf("⏰ **Jadwal stream**: %s\nMulai %s (%s)\n%s", title, stampFull(start), stamp(start), url)
}

func StreamLive(title, url string) string {
	return fmt.Sprintf("🔴 **Sedang live!** %s\n%s", escape(title), url)
}

func StreamEnded(title, url string) string {
	return fmt.Sprintf("⚫ **Stream selesai**: %s\n%s", escape(title), url)
}

// PingUpcoming is the separate message that mentions the "new waiting room" role. The link is in <angle brackets> so Discord does not show a second preview; the announcement itself already has one.
func PingUpcoming(roleID, title, url string, start time.Time) string {
	title = escape(title)
	if start.IsZero() {
		return fmt.Sprintf("⏰ <@&%s> **Jadwal stream baru**: %s\n<%s>", roleID, title, url)
	}
	return fmt.Sprintf("⏰ <@&%s> **Jadwal stream baru**: %s\nMulai %s (%s)\n<%s>", roleID, title, stampFull(start), stamp(start), url)
}

// PingLive is the separate message that mentions the "live now" role.
func PingLive(roleID, title, url string) string {
	return fmt.Sprintf("🔴 <@&%s> **Sedang live!** %s\n<%s>", roleID, escape(title), url)
}

// DevStreamSet is the reply to /devstream.
func DevStreamSet(state string) string {
	return fmt.Sprintf("Stream dev sekarang: %s", state)
}

// DevCheckFailed is shown when YouTube could not be read. The detail comes from the youtube package, which never includes the API key.
func DevCheckFailed(detail string) string {
	return "Gagal mengecek YouTube: " + detail
}

func DevCheckNone(checked int) string {
	return fmt.Sprintf("Dari %d video terbaru, tidak ada yang stream.", checked)
}

// DevCheckSummary shows the totals, so a count can be compared with YouTube's own Live tab at a glance. Premieres are counted apart.
func DevCheckSummary(checked, upcoming, live, ended, premieres int) string {
	return fmt.Sprintf("Dari %d video terbaru: %d stream (⏰ %d · 🔴 %d · ⚫ %d), %d premiere:",
		checked, upcoming+live+ended, upcoming, live, ended, premieres)
}

// DevCheckNote is a warning from the scan, shown under the list.
func DevCheckNote(note string) string {
	return "⚠️ " + note
}

// DevCheckLine is one stream in the /devcheck answer. icon says its state.
func DevCheckLine(icon, title string, start time.Time) string {
	title = escape(shorten(title, 60)) // cut first, so an escape is never cut in half
	if start.IsZero() {
		return fmt.Sprintf("%s %s", icon, title)
	}
	return fmt.Sprintf("%s %s · %s", icon, title, stamp(start))
}

func DevCheckMore(n int) string {
	return fmt.Sprintf("…dan %d lainnya", n)
}

// ---- Weekly recap ----------------------------------------------------------

// RecapStream is one stream in the recap. Times are already in WIB.
type RecapStream struct {
	Title    string
	URL      string
	Start    time.Time
	Duration time.Duration // 0 when unknown
}

// RecapDay is one calendar day (WIB) of the week.
type RecapDay struct {
	Day   time.Time
	Count int
	Total time.Duration
}

// RecapWeek is everything the recap says. The recap package fills it in; this package only decides how it reads. A nil Longest or Busiest means the line is left out because it would add nothing (one stream, or one day).
type RecapWeek struct {
	From, To time.Time // the window, in WIB
	Streams  []RecapStream
	Total    time.Duration
	Longest  *RecapStream
	Busiest  *RecapDay
}

// A Discord message holds at most 2000 characters; stay clear of it.
const recapMaxLen = 1900

// recapTitleMax keeps one very long title from taking a whole message.
const recapTitleMax = 70

// recapWatchLabel is the visible text of every link in the recap.
const recapWatchLabel = "tonton"

var (
	dayNames   = [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	monthNames = [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
)

// Recap is the weekly recap, split into messages that each fit in Discord.
// The first holds the summary and the start of the list; the rest continue it.
func Recap(w RecapWeek) []string {
	head := fmt.Sprintf("📊 **Rekap mingguan** · %s – %s", shortDate(w.From), shortDate(w.To))

	if len(w.Streams) == 0 {
		return []string{head + "\nMinggu ini Yuura tidak stream."}
	}

	var b strings.Builder
	b.WriteString(head + "\n")
	b.WriteString(fmt.Sprintf("Yuura stream **%d kali**", len(w.Streams)))
	if w.Total > 0 {
		b.WriteString(fmt.Sprintf(", total **%s**", duration(w.Total)))
	}
	b.WriteString(".")
	if w.Longest != nil {
		b.WriteString(fmt.Sprintf("\n🏆 Terpanjang: %s (%s)%s", recapTitle(*w.Longest), duration(w.Longest.Duration), recapWatch(*w.Longest)))
	}
	if w.Busiest != nil {
		b.WriteString(fmt.Sprintf("\n📅 Hari tersibuk: **%s**, %s (%d stream · %s)",
			dayNames[w.Busiest.Day.Weekday()], shortDate(w.Busiest.Day), w.Busiest.Count, duration(w.Busiest.Total)))
	}
	b.WriteString("\n\n**Daftar stream**")

	// Fill messages line by line; a line is never cut in half.
	out := []string{}
	cur := b.String()
	for _, st := range w.Streams {
		line := "\n" + recapLine(st)
		if utf8.RuneCountInString(cur)+utf8.RuneCountInString(line) > recapMaxLen {
			out = append(out, cur)
			cur = strings.TrimPrefix(line, "\n")
			continue
		}
		cur += line
	}
	return append(out, cur)
}

// recapTitle is the title as plain, escaped text. It is deliberately NOT the text of a link: Discord left "[title](<url>)" as raw characters whenever the title held an emoji (🧡), so the title stays outside the brackets.
func recapTitle(st RecapStream) string {
	return escape(shorten(st.Title, recapTitleMax)) // cut first, so an escape is never cut in half
}

// recapWatch is the link to the stream, with a fixed label that can never break the link. The <...> around the address stops Discord from adding a video preview for every line. It starts with " · " and is empty without an address.
func recapWatch(st RecapStream) string {
	if st.URL == "" {
		return ""
	}
	return fmt.Sprintf(" · [%s](<%s>)", recapWatchLabel, st.URL)
}

func recapLine(st RecapStream) string {
	line := fmt.Sprintf("• %s %s · %s", dayNames[st.Start.Weekday()][:3], shortDate(st.Start), recapTitle(st))
	if st.Duration > 0 {
		line += " · " + duration(st.Duration)
	}
	return line + recapWatch(st)
}

func shortDate(t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), monthNames[t.Month()-1])
}

// duration reads like "3 jam 20 menit 5 detik", rounded to the second. A part that is zero is left out ("1 jam 5 detik"), and 0 reads as "0 detik".
func duration(d time.Duration) string {
	total := int((d + 500*time.Millisecond) / time.Second)
	h, m, sec := total/3600, total%3600/60, total%60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%d jam", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%d menit", m))
	}
	if sec > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d detik", sec))
	}
	return strings.Join(parts, " ")
}

// DevRecapDescription and friends are the texts of /devrecap.
const (
	DevRecapDescription      = "Kirim pratinjau rekap mingguan ke channel ini (khusus dev)"
	DevRecapWeeksDescription = "Berapa minggu ke belakang (0 = rekap terakhir, bawaan 0)"
	DevRecapSent             = "Pratinjau rekap sudah dikirim."
	DevRecapOff              = "Rekap belum aktif. Isi YOUTUBE_API_KEY dan YOUTUBE_CHANNEL_ID di .env lalu jalankan ulang bot."
)

// shorten cuts s to at most max characters (not bytes, so no emoji or Japanese character is ever cut in half).
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// Pong answers /ping. The gateway latency is 0 until Discord has answered the first heartbeat, so it is left out then.
func Pong(latency time.Duration) string {
	if latency <= 0 {
		return "Pong! Bot hidup."
	}
	return fmt.Sprintf("Pong! Bot hidup. Latensi gateway: %d ms.", latency.Milliseconds())
}

// ---- Ops (English, dev only) ----------------------------------------------

// stamp renders a Discord timestamp, which every viewer sees as a live relative time such as "2 minutes ago".
func stamp(t time.Time) string { return fmt.Sprintf("<t:%d:R>", t.Unix()) }

// stampFull is the same idea with the "f" style: a full date and time.
func stampFull(t time.Time) string { return fmt.Sprintf("<t:%d:f>", t.Unix()) }

// HeartbeatOnline is the text of the one message the heartbeat keeps editing.
func HeartbeatOnline(version string, started, now time.Time) string {
	return fmt.Sprintf("**Online** · v%s\nStarted %s · last beat %s", version, stamp(started), stamp(now))
}

// HeartbeatOffline replaces it on a clean shutdown. If the message is stuck on "Online" with an old "last beat", the bot died without shutting down.
func HeartbeatOffline(version string, now time.Time) string {
	return fmt.Sprintf("**Offline** · v%s\nStopped cleanly %s", version, stamp(now))
}
