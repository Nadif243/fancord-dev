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

// The three messages for a collab on somebody else's channel. They follow the same life as her own streams: waiting room, live, ended. A premiere (a finished video released with a countdown) says so.

func CollabUpcoming(title, channel, url string, start time.Time, premiere bool) string {
	head := fmt.Sprintf("🤝 **Collab Yuura di channel lain**: %s\n📺 %s", escape(title), escape(shorten(channel, 80)))
	if premiere {
		head = fmt.Sprintf("🤝 **Collab Yuura di channel lain (premiere)**: %s\n📺 %s", escape(title), escape(shorten(channel, 80)))
	}
	if start.IsZero() {
		return head + "\n" + url
	}
	return fmt.Sprintf("%s\nMulai %s (%s)\n%s", head, stampFull(start), stamp(start), url)
}

func CollabLive(title, channel, url string, premiere bool) string {
	word := "Collab sedang live!"
	if premiere {
		word = "Premiere collab sedang tayang!"
	}
	return fmt.Sprintf("🔴 **%s** %s\n📺 %s\n%s", word, escape(title), escape(shorten(channel, 80)), url)
}

func CollabEnded(title, channel, url string, premiere bool) string {
	word := "Collab selesai"
	if premiere {
		word = "Premiere collab selesai"
	}
	return fmt.Sprintf("⚫ **%s**: %s\n📺 %s\n%s", word, escape(title), escape(shorten(channel, 80)), url)
}

// PingCollabUpcoming and PingCollabLive are the role pings for a collab, sent to the same roles as the pings for her own streams.
func PingCollabUpcoming(roleID, title, channel, url string, start time.Time, premiere bool) string {
	word := "Jadwal collab baru"
	if premiere {
		word = "Jadwal premiere collab baru"
	}
	text := fmt.Sprintf("⏰ <@&%s> **%s**: %s\n📺 %s", roleID, word, escape(title), escape(shorten(channel, 80)))
	if !start.IsZero() {
		text += fmt.Sprintf("\nMulai %s (%s)", stampFull(start), stamp(start))
	}
	return text + "\n<" + url + ">"
}

func PingCollabLive(roleID, title, channel, url string, premiere bool) string {
	word := "Collab sedang live!"
	if premiere {
		word = "Premiere collab sedang tayang!"
	}
	return fmt.Sprintf("🔴 <@&%s> **%s** %s\n📺 %s\n<%s>", roleID, word, escape(title), escape(shorten(channel, 80)), url)
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
	With     []string      // who was in it, already shortened (a full team is one name)
	Channel  string        // set for a collab on another channel: whose channel it was
}

// RecapDay is one calendar day (WIB) of the week.
type RecapDay struct {
	Day   time.Time
	Count int
	Total time.Duration
}

// RecapCollab is one collab partner and the number of streams they were in.
type RecapCollab struct {
	Name    string
	Streams int
	Days    []time.Time // the days (WIB) of those streams, oldest first, each once
}

// RecapWeek is everything the recap says. The recap package fills it in; this package only decides how it reads. A nil Longest or Busiest means the line is left out because it would add nothing (one stream, or one day).
type RecapWeek struct {
	From, To time.Time // the window, in WIB
	Streams  []RecapStream
	Total    time.Duration
	Longest  *RecapStream
	Busiest  *RecapDay
	Collabs  []RecapCollab // most streams first; empty = the line is left out
	Guests   []RecapStream // collabs on other channels, oldest first
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

	if len(w.Streams) == 0 && len(w.Guests) == 0 {
		return []string{head + "\nMinggu ini Yuura tidak stream."}
	}

	var b strings.Builder
	b.WriteString(head + "\n")
	if len(w.Streams) == 0 {
		b.WriteString("Minggu ini Yuura tidak stream di channelnya sendiri.")
	} else {
		b.WriteString(fmt.Sprintf("Yuura stream **%d kali**", len(w.Streams)))
		if w.Total > 0 {
			b.WriteString(fmt.Sprintf(", total **%s**", duration(w.Total)))
		}
		b.WriteString(".")
	}
	if w.Longest != nil {
		b.WriteString(fmt.Sprintf("\n🏆 Terpanjang: %s (%s)%s", recapTitle(*w.Longest), duration(w.Longest.Duration), recapWatch(*w.Longest)))
	}
	if w.Busiest != nil {
		b.WriteString(fmt.Sprintf("\n📅 Hari tersibuk: **%s**, %s (%d stream · %s)",
			dayNames[w.Busiest.Day.Weekday()], shortDate(w.Busiest.Day), w.Busiest.Count, duration(w.Busiest.Total)))
	}
	if len(w.Collabs) > 0 {
		b.WriteString("\n🤝 Kolaborasi: " + collabList(w.Collabs))
	}
	var lines []string
	if len(w.Streams) > 0 {
		b.WriteString("\n\n**Daftar stream**")
		for _, st := range w.Streams {
			lines = append(lines, recapLine(st))
		}
	}
	if len(w.Guests) > 0 {
		if len(lines) == 0 {
			b.WriteString("\n\n**Kolaborasi di channel lain**")
		} else {
			lines = append(lines, "\n**Kolaborasi di channel lain**")
		}
		for _, st := range w.Guests {
			lines = append(lines, recapLine(st))
		}
	}
	return splitLines(b.String(), lines)
}

// splitLines puts the head and then the lines into messages that each fit in Discord. A line is never cut in half, and a continuation message does not start with a blank line.
func splitLines(head string, lines []string) []string {
	out := []string{}
	cur := head
	for _, l := range lines {
		line := "\n" + l
		if utf8.RuneCountInString(cur)+utf8.RuneCountInString(line) > recapMaxLen {
			out = append(out, cur)
			cur = strings.TrimPrefix(l, "\n") // a continuation does not start with a blank line
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

// recapMaxCollabs keeps the summary short in a week with many guests.
const recapMaxCollabs = 10

// dayList reads like "; Min, Jum", or nothing when no days are known.
func dayList(days []time.Time) string {
	if len(days) == 0 {
		return ""
	}
	names := make([]string, len(days))
	for i, d := range days {
		names[i] = dayNames[d.Weekday()][:3]
	}
	return "; " + strings.Join(names, ", ")
}

// collabList reads like "@A (3×), @B (1×) dan 4 lainnya".
func collabList(collabs []RecapCollab) string {
	shown := collabs[:min(len(collabs), recapMaxCollabs)]
	parts := make([]string, len(shown))
	for i, c := range shown {
		parts[i] = fmt.Sprintf("%s (%d×%s)", escape(shorten(c.Name, 40)), c.Streams, dayList(c.Days))
	}
	out := strings.Join(parts, ", ")
	if rest := len(collabs) - len(shown); rest > 0 {
		out += fmt.Sprintf(" dan %d lainnya", rest)
	}
	return out
}

func recapLine(st RecapStream) string {
	line := fmt.Sprintf("• %s %s · %s", dayNames[st.Start.Weekday()][:3], shortDate(st.Start), recapTitle(st))
	if st.Duration > 0 {
		line += " · " + duration(st.Duration)
	}
	if st.Channel != "" {
		line += " · di " + escape(shorten(st.Channel, 40))
	}
	if len(st.With) > 0 {
		line += " · bareng " + escapeAll(st.With)
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

// RecapUnknownHandles is the ops note (English) that follows a recap when titles hold @handles that are not in the partners file. The handles come from titles, so they are escaped like any other outside text.
func RecapUnknownHandles(handles []string) string {
	const maxHandles = 15
	shown := handles[:min(len(handles), maxHandles)]
	parts := make([]string, len(shown))
	for i, h := range shown {
		parts[i] = escape("@" + h)
	}
	out := "⚠️ These handles are in this week's stream titles but not in partners.csv: " + strings.Join(parts, ", ")
	if rest := len(handles) - len(shown); rest > 0 {
		out += fmt.Sprintf(" and %d more", rest)
	}
	return out + ". Add them if they are collab partners (or as kind \"ignore\" if not)."
}

// DevRecapDescription and friends are the texts of /devrecap.
const (
	DevRecapDescription      = "Kirim pratinjau rekap mingguan ke channel ini (khusus dev)"
	DevRecapWeeksDescription = "Minggu: 0 = minggu berjalan sejauh ini (bawaan), 1 = rekap terakhir yang sudah selesai, dst."
	DevRecapSent             = "Pratinjau rekap sudah dikirim."
	DevRecapOff              = "Rekap belum aktif. Isi YOUTUBE_API_KEY dan YOUTUBE_CHANNEL_ID di .env lalu jalankan ulang bot."
)

// ---- Collab tagging (English, dev channel only) ---------------------------

const (
	CollabDescription       = "Tag who was in a stream with Yuura (dev only)"
	CollabStreamDescription = "The stream: pick one from the list, or paste its YouTube link"
	CollabAddDescription    = "Partners or teams who were there, separated by commas"
	CollabRemoveDescription = "Partners or teams who were NOT there, separated by commas"
	CollabOff               = "Collab tagging is off. Set YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID in .env and restart the bot."
	CollabNoStream          = "I don't know that stream. Pick one from the list that appears while typing, or paste the YouTube link of a stream the bot has seen."
	CollabNotSaved          = "⚠️ It could not be saved to disk. It holds until the bot restarts, so please try again."
)

// zoneWIB is Western Indonesia Time, which Indonesia keeps all year.
var zoneWIB = time.FixedZone("WIB", 7*60*60)

// dateLabel reads like "Sel 20 Okt", in WIB. An unknown time reads "?".
func dateLabel(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	t = t.In(zoneWIB)
	return dayNames[t.Weekday()][:3] + " " + shortDate(t)
}

// CollabStreamLabel is one line in the list of streams that appears while typing /collab. It is plain text (Discord does not format these labels), at most 100 characters, with a 🤝 when somebody is already tagged and a ✅ when a person has checked the stream.
func CollabStreamLabel(start time.Time, title string, tagged, checked bool) string {
	label := dateLabel(start) + " · " + strings.Join(strings.Fields(title), " ")
	if tagged {
		label = "🤝 " + label
	}
	if checked {
		label = "✅ " + label
	}
	return shorten(label, 100)
}

// CollabNameLabel is one partner or team in the list that appears while typing a name. members is 0 for a partner and the team size for a team.
func CollabNameLabel(name string, members int) string {
	if members > 0 {
		return fmt.Sprintf("👥 %s (%d)", name, members)
	}
	return name
}

// CollabDone answers /collab: the stream, who is tagged now, and what changed.
func CollabDone(title string, start time.Time, partners, added, removed []string, by string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🤝 **%s** · %s\n", dateLabel(start), escape(shorten(title, 80)))
	if len(partners) == 0 {
		b.WriteString("Collab partners: none")
	} else {
		b.WriteString("Collab partners: " + escapeAll(partners))
	}
	if len(added) > 0 {
		b.WriteString("\n➕ " + escapeAll(added))
	}
	if len(removed) > 0 {
		b.WriteString("\n➖ " + escapeAll(removed))
	}
	if len(added) == 0 && len(removed) == 0 {
		b.WriteString("\nNo change.")
	}
	b.WriteString("\n✅ Checked")
	if by != "" {
		b.WriteString(" by " + escape(by))
	}
	return b.String()
}

// CollabReminder is the Saturday message (English, dev channel): the streams of the week from..to (the one the next recap covers) that nobody has checked yet, each with who the bot has tagged so far (after "bareng") and a link to watch. total is all of that week's streams so far. It is split into several messages when it is long.
func CollabReminder(from, to time.Time, total int, streams []RecapStream) []string {
	head := fmt.Sprintf("📝 **Collab check** · %s – %s · %d of %d streams have not been checked yet. The recap is posted on Sunday at 20:00 WIB.\n"+
		"Run `/collab` on each: add who was there, take off who was not, or run it with nothing added if the line below is right as it is. "+
		"\"bareng …\" is who the bot has tagged so far.\n", shortDate(from.In(zoneWIB)), shortDate(to.In(zoneWIB)), len(streams), total)
	lines := make([]string, len(streams))
	for i, st := range streams {
		lines[i] = recapLine(st)
	}
	return splitLines(head, lines)
}

// CollabReminderNone answers /devremind when there is nothing to check.
func CollabReminderNone(total int) string {
	if total == 0 {
		return "Nothing to check: no stream has ended since the last recap."
	}
	return fmt.Sprintf("Nothing to check: all %d streams since the last recap are checked. ✅", total)
}

// MissedStream is a stream that ended while the bot was off.
type MissedStream struct {
	Stream    RecapStream
	Announced bool // it had been announced earlier, and its message is now updated
}

// BackOnline is the note (English, ops channel) the bot posts when it starts again after being off: for how long, and which streams ended meanwhile. The streams are in the history and count in the recap, but one that was never announced got no message in the announcement channel. Long lists are split into several messages.
func BackOnline(offSince, now time.Time, missed []MissedStream) []string {
	head := fmt.Sprintf("🔄 **Back online** · off for %s, since %s.", offFor(now.Sub(offSince)), clockLabel(offSince))
	if len(missed) == 0 {
		return []string{head + "\nNo stream ended meanwhile."}
	}
	noun := "streams"
	if len(missed) == 1 {
		noun = "stream"
	}
	head += fmt.Sprintf("\n%d %s ended while the bot was off. They are in the history and count in the recap; one marked \"not announced\" got no message in the announcement channel.\n", len(missed), noun)
	lines := make([]string, len(missed))
	for i, m := range missed {
		lines[i] = recapLine(m.Stream)
		if !m.Announced {
			lines[i] += " · *not announced*"
		}
	}
	return splitLines(head, lines)
}

// offFor says how long the bot was off, to the minute.
func offFor(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	return duration(d.Round(time.Minute))
}

// clockLabel is a day and a time of day in WIB, like "Kam 29 Okt 03:41 WIB".
func clockLabel(t time.Time) string {
	return dateLabel(t) + " " + t.In(zoneWIB).Format("15:04") + " WIB"
}

// CollabCandidateView is what a reviewer is shown about a video on another channel.
type CollabCandidateView struct {
	Title        string
	ChannelTitle string
	URL          string
	State        string // "upcoming", "live", "ended", or "" for an ordinary video
	Premiere     bool
	Start        time.Time
	Mentions     bool     // the title or description names her
	Partners     []string // partners.csv names that match
	Source       string   // "tweet" or "manual"
	SourceRef    string   // the X post ID, or who sent it
}

// CollabCandidate is the review message (English, for the reviewers). The buttons are attached by the caller. The link is plain so Discord shows the video's preview.
func CollabCandidate(v CollabCandidateView) string {
	origin := "sent by " + escape(v.SourceRef)
	switch v.Source {
	case "tweet":
		origin = "found in her X post <https://x.com/i/status/" + escape(v.SourceRef) + ">"
	case "xlink":
		origin = "found in an X post a fan sent <https://x.com/i/status/" + escape(v.SourceRef) + ">"
	}
	partner := "not in partners.csv"
	if len(v.Partners) > 0 {
		partner = escapeAll(v.Partners)
	}
	mentions := "no"
	if v.Mentions {
		mentions = "yes"
	}
	return fmt.Sprintf("🔎 **Collab candidate** · %s\n**%s**\n📺 %s · %s\n🎤 Mentions Yuura: %s · Partner: %s\n%s",
		origin, escape(shorten(v.Title, 150)), escape(shorten(v.ChannelTitle, 80)), collabStateLine(v), mentions, partner, v.URL)
}

func collabStateLine(v CollabCandidateView) string {
	what := "stream"
	if v.Premiere {
		what = "premiere"
	}
	switch v.State {
	case "upcoming":
		if v.Start.IsZero() {
			return "waiting room · start time not set"
		}
		return "waiting room (" + what + ") · " + clockLabel(v.Start)
	case "live":
		return what + " live now"
	case "ended":
		return what + " ended"
	}
	return "ordinary video, not a stream"
}

// CollabDecided is the review message after a decision: the same text, with the verdict added at the end (the buttons are removed by the caller).
// decision is "confirmed", "ignored" or "channel-ignored".
func CollabDecided(v CollabCandidateView, decision, by string) string {
	verdict := "✅ **Confirmed** by " + escape(by)
	switch decision {
	case "ignored":
		verdict = "🚫 **Ignored** by " + escape(by)
	case "channel-ignored":
		verdict = "🚫 **Ignored**, and this channel will not be shown again, by " + escape(by)
	}
	return CollabCandidate(v) + "\n" + verdict
}

// ---- /collablink (Indonesian, for fans) -----------------------------------

const (
	CollabLinkDescription       = "Kirim link YouTube kolaborasi Yuura di channel lain untuk dicek"
	CollabLinkOptionDescription = "Link video atau waiting room YouTube"
	CollabLinkNone              = "Aku tidak menemukan link YouTube di situ. Kirim link videonya langsung, misalnya https://www.youtube.com/watch?v=..."
	CollabLinkKnown             = "Link itu sudah pernah masuk daftar, terima kasih!"
	CollabLinkSlow              = "Pelan-pelan ya, tunggu sebentar sebelum kirim link lagi."
	CollabLinkOff               = "Fitur ini belum aktif."

	CollabLinkXNoLink   = "Tweet itu tidak berisi link YouTube. Kirim link YouTube-nya langsung ya."
	CollabLinkXCap      = "Batas pengecekan link X bulan ini sudah tercapai. Kirim link YouTube-nya langsung ya."
	CollabLinkXFailed   = "Aku belum bisa membaca tweet itu sekarang. Kirim link YouTube-nya langsung ya."
	CollabLinkXNotFound = "Tweet itu tidak ditemukan (mungkin dihapus, atau akunnya privat)."
	CollabLinkXNotHere  = "Di sini hanya link YouTube yang bisa dipakai. Kirim link YouTube-nya ya."
)

// CollabLinkThanks answers a fan whose link was queued for the reviewers.
func CollabLinkThanks(n int) string {
	if n > 1 {
		return fmt.Sprintf("Terima kasih! %d link sudah masuk antrean untuk dicek moderator.", n)
	}
	return "Terima kasih! Linknya sudah masuk antrean untuk dicek moderator."
}

// CollabReviewDenied is the answer to somebody without the reviewer role who pressed a button (English; only they see it).
const CollabReviewDenied = "Only members with the reviewer role can confirm or ignore collabs."

// CollabReviewOff says the button no longer works for this video.
const CollabReviewOff = "This candidate is no longer known to the bot."

// TweetRelay is the message for one of her X posts in the tweets channel (Indonesian, for fans). The link is plain so Discord shows its preview.
// kind is "post", "reply", "quote" or "repost".
func TweetRelay(kind, url string) string {
	label := "🐦 **Tweet baru dari Yuura**"
	switch kind {
	case "reply":
		label = "💬 **Yuura membalas**"
	case "quote":
		label = "💭 **Yuura mengutip**"
	case "repost":
		label = "🔁 **Yuura me-repost**"
	}
	return label + "\n" + url
}

// RelayFailing is the ops note (English) when the tweet relay keeps failing.
func RelayFailing(failures int, err string) string {
	return fmt.Sprintf("⚠️ **Tweet relay** · %d checks in a row have failed, so new tweets are not being relayed. It keeps trying; this note comes once.\nLast error: %s", failures, escape(shorten(err, 300)))
}

// RelayNoCredits is the ops note when X says the account has no credits.
func RelayNoCredits() string {
	return "💳 **Tweet relay** · X says the account has no credits, so new tweets are not being relayed. Add credit in the X developer console. The bot asks again once an hour (restart it to ask right away); this note comes once."
}

// RelayBudgetWarning is the ops note when most of the monthly X read cap is used.
func RelayBudgetWarning(reads, cap int) string {
	return fmt.Sprintf("💸 **Tweet relay** · %d of %d X reads used this month. It pauses at the cap and resumes next month; raise X_MONTHLY_READ_CAP to allow more.", reads, cap)
}

// RelayCapReached is the ops note when the relay pauses at the cap.
func RelayCapReached(reads, cap int) string {
	return fmt.Sprintf("⏸️ **Tweet relay paused** · %d of %d X reads used this month. Tweets are not relayed until next month. Raise X_MONTHLY_READ_CAP to continue now.", reads, cap)
}

// DevRemindDescription and friends are the texts of /devremind.
const (
	DevRemindDescription = "Post the collab reminder for this week now (dev only)"
	DevRemindSent        = "Reminder posted."
	DevRemindOff         = "The reminder is off. It needs YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID in .env."
)

// CollabUnknown answers /collab when some typed names are not on the list.
func CollabUnknown(unknown []string) string {
	return "I don't recognise: " + escapeAll(unknown) + ". Nothing was changed.\n" +
		"Names must be on the partner list (partners.csv): a name, handle or nickname from it. Pick from the suggestions while typing, or add the person to the file and restart the bot."
}

// escapeAll escapes each text and joins them with commas.
func escapeAll(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = escape(s)
	}
	return strings.Join(parts, ", ")
}

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

// TweetRelayPing is TweetRelay with the role mentioned in front, for the message that pings. roleID is a Discord role ID.
func TweetRelayPing(roleID, kind, url string) string {
	return "<@&" + roleID + "> " + TweetRelay(kind, url)
}

// ---- /rodok (Indonesian, for fans) ----------------------------------------

const (
	RodokDescription       = "Merodok seseorang"
	RodokOptionDescription = "Siapa yang mau dirodok"
	RodokGuildOnly         = "Perintah ini hanya bisa dipakai di server."

	// PLACEHOLDERS: Change the text here; nothing else needs to move.
	RodokSelf = "Malu dikit, masa gooning di publik."
	RodokBot  = "Don't be a clanker?? :(( #RobotLivesMatter"
)

// Rodok is the public answer. who and target are Discord user IDs, shown as mentions (the message is sent so that they ping nobody).
func Rodok(who, target string, n int) string {
	return fmt.Sprintf("<@%s> merodok <@%s> **%dx**", who, target, n)
}

// RodokCooling and RodokLocked are PLACEHOLDERS too (see above). wait is how long until the person may try again.
func RodokCooling(wait time.Duration) string {
	secs := int((wait + time.Second - 1) / time.Second) // round up, so never "0 detik"
	return fmt.Sprintf("Terlalu jomok, tunggu %d detik lagi.", secs)
}

func RodokLocked(wait time.Duration) string {
	return fmt.Sprintf("Jomok bgt njirr. Sabar, boleh rodok %s detik lagi.", duration(wait))
}

// ---- /setup (English: for the fancord head and staff) ---------------------

const (
	SetupDescription        = "Change the bot's channels and roles (Manage Server only)"
	SetupShowDescription    = "Show where the bot posts now"
	SetupChannelDescription = "Set a channel the bot posts in or reads from"
	SetupRoleDescription    = "Set a role the bot pings or checks"
	SetupResetDescription   = "Go back to what .env says for one setting"
	SetupWhichDescription   = "What to change"
	SetupChannelOption      = "The channel"
	SetupRoleOption         = "The role"

	SetupDenied = "Only members with Manage Server (or Administrator) can use /setup."
	SetupOff    = "/setup is not available."
	SetupBad    = "I could not read that. Pick a setting from the list and a channel or role."

	SetupEveryone = "That is the @everyone role. Pick a real role instead."
)

// SetupRow is one line of /setup show. Mention is a ready channel or role mention ("" when nothing is set); Note says where it comes from.
type SetupRow struct{ Label, Mention, Note string }

// SetupShow lists every setting, then any warnings.
func SetupShow(rows []SetupRow, warnings []string) string {
	var b strings.Builder
	b.WriteString("**Current settings**\n")
	for _, r := range rows {
		v := r.Mention
		if v == "" {
			v = "not set"
		}
		fmt.Fprintf(&b, "%s: %s", r.Label, v)
		if r.Note != "" {
			fmt.Fprintf(&b, " (%s)", r.Note)
		}
		b.WriteByte('\n')
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "\n%s", w)
	}
	return strings.TrimRight(b.String(), "\n")
}

// SetupSaved confirms a change and adds the notes that matter for it.
func SetupSaved(label, mention string, notes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** is now %s. It applies from the next message.", label, mention)
	for _, n := range notes {
		fmt.Fprintf(&b, "\n%s", n)
	}
	return b.String()
}

// SetupReset confirms that a saved value is gone. now is a mention, or "" when nothing is set any more.
func SetupReset(label, now string, hadSaved bool) string {
	if !hadSaved {
		return fmt.Sprintf("**%s** was not changed by /setup, so there is nothing to reset.", label)
	}
	if now == "" {
		return fmt.Sprintf("**%s** is back to .env, which sets nothing for it, so it is off.", label)
	}
	return fmt.Sprintf("**%s** is back to %s (what .env says).", label, now)
}

// Warnings and notes. %s are mentions.
const (
	SetupNoSave = "It is in use now, but it could not be written to the settings file, so a restart would forget it."

	SetupPermsPost = "I may not be able to post in %s. I need View Channel and Send Messages there; check the channel's permissions."
	SetupPermsFans = "I may not be able to work in %s. I need View Channel, Send Messages, Add Reactions and Read Message History there; check the channel's permissions."

	SetupNoteAnnounce  = "A stream already announced keeps being updated in the channel it was posted in. The recap and tweets follow this channel unless they have their own."
	SetupNoteReview    = "Candidates already waiting keep their buttons where they are; new ones go here."
	SetupNoteOps       = "The heartbeat posts a fresh message there on its next beat; the old one stays where it was."
	SetupNoteStaff     = "Dev commands and /collab now work only in %s. Review, ops and the Saturday reminder follow it unless they have their own channel."
	SetupNoteReminder  = "The Saturday reminder goes here from now on."
	SetupNotePing      = "This one role is pinged both for a waiting room and when she goes live. It must allow \"anyone can mention this role\", or the ping will not notify anyone."
	SetupNoteTweetPing = "This role is pinged for every tweet the bot relays (posts, replies, quotes and reposts). It must allow \"anyone can mention this role\", or the ping will not notify anyone."
	SetupNoteReviewer  = "Only members with this role can press Confirm and Ignore now."

	SetupNoteFansRestart = "The bot did not ask Discord for message content when it started, so it cannot read this channel yet. Turn on Message Content Intent in the Developer Portal (Bot > Privileged Gateway Intents), then restart the bot once. After that it works without restarts."
	SetupNoteFansNoYT    = "Collab review is off (it needs YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID), so this channel does nothing yet."

	SetupWarnFansRestart = "Fan channel: set, but the bot is not reading message content yet. It needs the Message Content Intent switched on in the Developer Portal and one restart."
	SetupWarnFansNoYT    = "Fan channel: set, but collab review is off (it needs YOUTUBE_API_KEY and YOUTUBE_CHANNEL_ID)."
)
