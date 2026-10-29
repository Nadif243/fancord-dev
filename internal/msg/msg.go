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
	With     []string      // who was in it, already shortened (a full team is one name)
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
	if len(w.Collabs) > 0 {
		b.WriteString("\n🤝 Kolaborasi: " + collabList(w.Collabs))
	}
	b.WriteString("\n\n**Daftar stream**")

	lines := make([]string, len(w.Streams))
	for i, st := range w.Streams {
		lines[i] = recapLine(st)
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
			cur = l
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
	DevRecapWeeksDescription = "Berapa minggu ke belakang (0 = rekap terakhir, bawaan 0)"
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

// CollabStreamLabel is one line in the list of streams that appears while typing /collab. It is plain text (Discord does not format these labels), at most 100 characters, with a 🤝 when somebody is already tagged  and a ✅ when a person has checked the stream.
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
