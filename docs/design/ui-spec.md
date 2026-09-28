# anywhere-file: UI specification for design

Source of truth for designing every screen of anywhere-file. Written from the code on `main`
(commit 7343b60), the open and closed GitHub issues, `docs/new-arch.md` and the ADRs.

Every screen, field, state and action below exists in the system, or is in an open issue with
a fixed scope. Each item carries a tag:

- `BUILT`: the code does this today. The copy in quotes is the exact text the app shows.
- `PLANNED #N`: an open issue defines it. Design it, and label the frame with the issue number.
- `NEW`: agreed with the product owner on 2026-09-28, and the issue is not filed yet. Design
  it. Section 12 lists these.
- `SERVER ONLY`: the server can do it, but no screen or issue asks for it. Do not design it
  unless told to.

Anything not in this document is not supported. Section 9 lists the things people most often
expect and that do not exist.

**Name.** The product name is not final. Wherever this document or the app says
"anywhere-file", the designs say **ProductName**. The web address is **productname.example**.
A rename later is then a find and replace.

---

## 1. What the product does

A person has one or more PCs at home. They want to reach files on those PCs from a phone or a
laptop.

1. On each PC they install the **agent**. It runs in the background with no window. They pick
   folders on that PC to share.
2. On a phone or laptop they install the **client app**. On the same Wi-Fi, the app finds the
   PCs by itself and opens their shared folders. No account and no Internet are needed.
3. To reach a PC from outside the house, the person makes an **account**, adds the PC to it,
   and signs in to the app. Traffic then goes through our **server**, which checks who they
   are and what they may reach.
4. The owner of a PC can **invite** someone else with a one-time code. That person becomes a
   **guest** on the PC.

---

## 2. The four surfaces

| Surface | Where it runs | Who uses it | Design priority |
|---|---|---|---|
| Client app | Android phone, and a desktop window on Windows, macOS and Linux. One shared UI. | Everyone who reaches files | Main. Full design. |
| PC settings page | A browser tab on the PC itself, at `http://127.0.0.1:<port>` | The person who owns the PC | Secondary. One page. |
| Website | Any browser | New visitors, and everyone for account tasks | Landing, downloads and help: main. Account pages: secondary. |
| Agent command line | A terminal on the PC | Technical users | Not designed. Mentioned only where a flow needs it. |

The client app only **consumes**. It never changes what a PC shares, and it never adds a PC to
an account. Both of those happen on the PC's own settings page. This is a fixed decision
(ADR 0006). Do not put "share a folder" or "add this PC" in the client app.

The website has **no** dashboard, no device management and no billing. Those are client app
screens or do not exist. The one account task on the website beyond sign up and password
reset is deleting the account (`NEW`, section 7).

**Server.** The owner runs one public server at productname.example (`NEW`). The app and the
agent have its address built in. A person never has to type a server address. Typing another
one is possible behind an "advanced" link, for testing.

**Distribution.** No app store. The Android APK and the desktop apps download from the
website. So the app cannot update itself, and it tells the person when a new version is out
(`NEW`, 5.2).

**Two separate downloads on a desktop.** "Share this PC" is the agent. "Reach your PCs" is
the client app. A person may need one or both. They are separate installers.

---

## 3. Words used in the UI

Keep these words. They are already in the app's copy.

| Word | Meaning |
|---|---|
| device / PC | A computer running the agent. The app lists "devices". Settings talk about "this PC". |
| shared folder | One folder a PC shares. Technically an "application" called by a short name, e.g. `files`. |
| "On your Wi-Fi" | The app reaches this device directly, on the same Wi-Fi or network. No account needed. |
| "Over the internet" | The app reaches this device through the ProductName server. Needs an account. |
| "Can't reach right now" | Neither path works: the PC is off, asleep, or not connected. |
| "Checking…" | The app is still finding out. |
| account | Email and password on our server. |
| admin, shown as "Manages it" / "You manage it" | May see who can reach the PC, remove people and invite people. |
| guest, shown as "Guest" | May reach the PC's folders. Sees no management. |
| invitation code, "code" | One-time secret an admin makes and sends to someone. |
| (never shown) "online", "LAN", "relay", "tunnel", "direct" | Technical words. Do not use them in the UI. The reach labels above replace them. |
| fingerprint | A short text form of the PC's identity, like `SHA256:q3v...`. The same text the PC prints with `agent key`. Used to check the PC is the real one. |

---

## 4. Data the UI can show

Only these fields exist. Do not add fields that are not in these tables.

### 4.1 A device on this network (client, `Device`)

| Field | Example | Notes |
|---|---|---|
| name | `pc1`, `Ana's MacBook Air.local` | Can be long. Two PCs can have the same name. |
| shared folders | `files`, `photos` | A list of short names. Lowercase letters, digits and hyphens, 32 characters at most. Can be empty. |
| address | `192.168.1.20:7433` | Tells two same-named PCs apart. |
| confirmed | yes / no | "No" while the app is still checking the PC. |
| refused | `This did not prove it is 4f3a1b2c, so it is a different device.` | Set when the PC failed the identity check. The card must show this text. |
| first contact | yes / no | Yes when this app has never seen this PC before. |
| fingerprint | `SHA256:q3vX...` (43 characters after the prefix) | Always derivable. Show it on first contact, behind a tap otherwise (#162). |

There is no OS, no icon, no disk size, no "last seen" time, no owner name.

### 4.2 A device on the account (client, `RemoteDevice`)

| Field | Example |
|---|---|
| name | `pc1` |
| online | yes / no |
| role | admin or guest |
| shared folders | `files` |

The list is fetched when the home screen appears and when the person taps Refresh. It does
not update live.

### 4.3 A person on a device (admin view, `DeviceUser`)

| Field | Example |
|---|---|
| email | `ewa@example.com` |
| role | admin or guest |

No display name, no avatar, no "last active", no "invited by".

### 4.4 An invitation

| Field | Example |
|---|---|
| code | `BOBIEyhfUSwH8MUL8nl42HMQKuAmgoWlMdKlKrRvIZ0` (43 characters) |
| role | guest or admin |
| until | a date and time |

The code is shown **once**, right after it is made. The server keeps only a hash. There is no
list of open invitations and no way to cancel one.

### 4.5 A file or folder in a shared folder (`Entry`, `Listing`)

| Field | Example |
|---|---|
| name | `holiday-2026.jpg` |
| is folder | yes / no |
| size | `2.4 MB` (files only) |
| modified | a date, in the platform's short date format |

Per folder listing: **can upload** (yes/no) and **can delete** (yes/no). Hide "Send a file"
and "Delete" when these are no.

No thumbnails, no file type from the server (a type icon can only come from the file name's
extension), no owner, no permissions per file, no preview.

### 4.6 The account (client)

| Field | Notes |
|---|---|
| server address | Built in (`NEW`). Can be changed behind "Use a different server". Starts with `http://` or `https://`. |
| email | Known once the server answered. |
| signed in | yes / no |
| ready | No while the app checks a saved session at start. Show a neutral state, not "signed out". |
| trouble | Last error, in words. |
| caveat | A sentence shown when the session is stored in a plain file because the system has no keychain. |

---

## 5. Client app screens

Material 3. Light and dark. On Android 12 and later the colours come from the phone's
wallpaper (dynamic colour). The same screens run on a phone and in a desktop window.

Navigation is simple: one home screen, and every other screen is opened from it and returns to
it with a back arrow (and the Android back gesture). There is no bottom bar, no drawer and no
tabs.

```text
Home ──► File browser ──► (Search, PLANNED #164)
  │
  ├──► Manage device (admins only)
  │
  ├──► Join with a code (dialog)
  │
  └──► Account (NEW) ──► Sign in
                     └──► About (NEW)

Android 17+ only: Local network permission ──► Home
```

### 5.1 Local network permission (Android 17 and later only) `BUILT`

Shown instead of Home until the person allows local network access.

- Heading: "Devices on this network"
- Text: "anywhere-file finds your devices by asking this network which of them are running it
  too. Android checks with you first."
- Button: "Allow". Opens Android's own permission prompt.
- If refused, add:
  - Text: "Without it, you can still pick one device at a time from Android's own list."
  - Button: "Choose a device". Opens Android's system device picker.
- Below that, in the same scrolling list: the device list as on Home (5.2). Devices on the
  account still show and can say "Over the internet", because that path does not need this
  permission. The Account icon is in the heading row, as on Home.

### 5.2 Home `NEW` layout, built on `BUILT` data

One list of devices. There are no "On this network" and "Away from home" sections. Each
device appears once, and its card says how the app reaches it right now.

1. Title row: "anywhere-file", then two icons at the end: Refresh, and Account (opens 5.8).
2. `NEW`: when a newer version is out, a banner under the title: "Version {x} is available."
   with a button "Download" that opens the website's download page in the browser, and a
   way to dismiss it until the next version. No automatic install. The app asks the website
   for the latest version number when Home opens.
3. Device cards, or one of the empty states.
4. Signed in: a button "Join with a code" under the list (opens 5.2.3).
   Signed out: a line "Sign in to reach your devices over the internet." and a button
   "Sign in".

#### 5.2.1 How the list is built

Two sources, joined by the device ID (the same PC has the same ID in both):

- Devices found on the same Wi-Fi or network. Live: they appear and go as PCs join and
  leave. Works signed out.
- Devices on the account, from the server. Signed in only. Fetched when Home opens and on
  Refresh. Not live.

A device found on the Wi-Fi that is not on the account still shows. It has no role line.

Order: "On your Wi-Fi" first, then "Over the internet", then "Can't reach right now". By name
inside each group.

#### 5.2.2 Device card

Top of the card: computer icon, name, and the reach label with a small dot.

| Reach label | When | Dot | Folders open? |
|---|---|---|---|
| "On your Wi-Fi" | Found on the network and it proved who it is. The app talks to it directly. Wins when both paths work. | filled | Yes, directly `BUILT` |
| "Over the internet" | On the account, the server says it is connected, and it is not on this network. | filled | Yes, through the server `PLANNED #103` |
| "Can't reach right now" | On the account, not connected to the server, and not on this network. | hollow | No. Tiles are shown but disabled. |
| "Checking…" | Just found on the network and still being checked, or the server list is still loading. | dotted | Not yet |

A separate error state, whatever the path:

| State | Look | Text |
|---|---|---|
| Refused | Error colour, warning icon instead of the computer icon, no tiles | "Not the device it says it is." and under it the reason, e.g. "This did not prove it is 4f3a1b2c, so it is a different device." |

Under the name and the reach label:

- Role line, for devices on the account only: "You manage it" or "Guest".
- "1 shared folder", "N shared folders", or "Nothing shared yet".
- One tile per shared folder: folder icon and name. Tapping a tile opens the File browser.
- For a device on your Wi-Fi: the address, in small text, e.g. `192.168.1.20:7433`. It tells
  two same-named PCs apart.

Admins: a "Manage" text button on the card opens Manage device (5.4). Guests and devices not
on the account have none.

Colour: a card that can be opened now is filled with the accent colour. The others stay
quiet. The label and the dot say the same thing as the colour, so colour is never the only
signal.

`NEW` First contact (#162 asked for it, the build dropped it): the first time this app sees a
PC on the Wi-Fi, show its fingerprint on the card with a short hint to compare it with what the
PC shows (`agent key` in a terminal on the PC). On a device seen before, the fingerprint is
behind a tap.

Empty states:

- First 5 seconds, nothing found yet: spinner and "Looking…"
- After 5 seconds, nothing found and nothing on the account: "No devices yet. One shows up
  here when anywhere-file is running on it and it is on this network."
- Server did not answer: an error line in error colour above the list, e.g. "The server did
  not answer." Devices on the Wi-Fi still show.

#### 5.2.3 Join with a code dialog `BUILT` behaviour, `NEW` placement

- Title: "Join with a code"
- Text: "Got a code from someone? It lets you reach their PC."
- Field: "Invitation code"
- Buttons: "Cancel", "Join". Join is disabled while the field is empty.
- While sending: a thin progress bar.
- On success: the dialog closes and the list refreshes. The new device appears with the role
  the code carried.
- On failure: the server's message, e.g. "invite invalid, expired or used".

### 5.3 Sign in `BUILT`

Top bar: back arrow, title "Sign in".

- Text: "An account reaches your PCs from anywhere, through the server. On this network it is
  not needed."
- Field "Server", a web address. Pre-filled with the last one used. `NEW`: hidden by default,
  because the hosted server is built in. A small text button "Use a different server" shows
  it. Most people never see it.
- Field "Email"
- Field "Password", hidden
- Progress bar while signing in
- Error line, for example:
  - "invalid email or password"
  - "Type the address of your server first."
  - "The address has to start with http:// or https://."
  - "Only the address goes here, with nothing after it."
- Button "Sign in", full width. Disabled until email and password are filled.
- Text button "Create an account". Opens the website's signup page in the system browser.
- Text button "Forgot your password". Opens the website's reset page in the system browser.

On success the screen closes and Home shows the signed-in account.

There is no sign-up form, no password reset form, no "remember me", no social login and no
two-factor step in the app.

### 5.4 Manage device (admins only) `BUILT`

Top bar: back arrow, the device name.

Progress bar and error line at the top when needed.

**Who can reach it**

One row per person:

- Email
- "Manages it" or "Guest"
- At the end: "You" for the signed-in person, otherwise a text button "Remove"

Remove opens a dialog:

- Title: "Remove ewa@example.com?"
- Text: "They lose access to pc1 straight away. A new code brings them back."
- Buttons: "Cancel", "Remove"

If this would remove the last admin, the server refuses with "a device keeps at least one
admin". The app does not offer to remove yourself.

There is no way to change someone's role. To change it, remove them and invite them again.

**Invite someone**

- "They join as": two chips, "Guest" (default) and "Admin"
- "The code works once, for": three chips, "1 hour", "1 day" (default), "7 days". The server
  allows 7 days at most.
- Button: "Make a code"

After making a code, a card in the accent colour:

- The code in monospace, selectable
- "Works once, until {date}. It is shown only now."
- Button "Share". On Android it opens the share sheet. On desktop it copies to the clipboard,
  and a line under the button says so.

The shared text is: "You're invited to reach pc1 with anywhere-file. Sign in at
{server} in the app, then enter this code under "Got a code from someone?": {code}. It works
once, until {date}."

`SERVER ONLY`: disabling a device. The server has it, there is no way to undo it, and no issue
asks for a screen. Do not design it.

### 5.5 File browser `BUILT`, with additions from `PLANNED #163 #164 #165 #103 #156`

Opened from a folder tile. Shows one shared folder of one PC.

#### Built today

Top bar:

- Back arrow. In a subfolder it goes up one folder. At the top it returns to Home.
- Title: the shared folder's name. Under it, in small text, the current path when not at the
  top, e.g. `holiday/2026`.
- Refresh icon.

Body states:

| State | Look |
|---|---|
| Loading | Centred spinner |
| Failed | Error text in the middle, e.g. "That device did not answer." or "That folder is not on the device any more.", and a button "Try again" |
| Empty at top | "This folder is empty." |
| Empty below top | "Nothing in holiday/2026." |
| Listing | One row per entry, divider between rows |

Rows:

- Folder: name, modified date, chevron. Tapping opens it.
- File: name (long names shortened in the middle), "2.4 MB · 3 Sep 2026", and a three-dot
  menu. Tapping the row saves the file.
  - Menu item "Save a copy here"
  - Menu item "Delete from the device", with a bin icon. Only when the folder allows delete.

Floating button "Send a file" with a plus icon. Only when the folder allows upload. It opens
the system file picker (Android) or a file dialog (desktop).

Transfers:

- One at a time. While one runs, a thin progress bar sits under the top bar. It does not show
  a percentage.
- When it ends, a snackbar says what happened: "Saved to /Users/ana/Downloads/a.txt", "Sent
  a.txt", "Deleted a.txt", or the error, e.g. "The device would not delete (403)."
- Files are saved to the phone's Downloads, or the desktop's Downloads folder. A name that is
  taken gets " (2)" added.

Delete has no confirmation today. A confirmation dialog is a good addition and needs nothing
new from the system.

#### `PLANNED #163`: breadcrumbs and file type icons

- A breadcrumb strip under the top bar. It scrolls sideways and keeps the deepest folder in
  view. Tapping a segment jumps straight to that folder.
- An icon at the start of every row, on a rounded tinted square. Types, chosen by file name:
  folder, image, video, audio, document, archive, and a fallback for anything else. Each type
  has its own tint.
- An icon above the text on the empty and failed states.
- Not included: thumbnails, grid view, compact view, file type filter chips.

#### `PLANNED #164`: search

- Opened from a search icon in the File browser's top bar. Its own screen.
- A search field. Results update as the person types, after a short pause.
- A scope switch with two options: this folder and everything under it, or the whole PC
  (every shared folder on it).
- A result row: the name, and the folder that holds it. With "whole PC", also the shared
  folder it came from.
- Tapping a file saves it. Tapping a folder closes search and opens the File browser in that
  folder.
- Matching ignores upper and lower case. Folders are results too.
- States: empty field, searching, no results, error.

#### `PLANNED #165`: select several files

- A long press on a row starts a selection. Then a tap adds or removes a row.
- The top bar becomes a selection bar: "3 selected", select all, save, delete, and a close
  button.
- Save and delete run one file at a time. The progress bar counts files: "3 of 12".
- A failed file does not stop the rest. At the end, one message names what failed.
- Leaving the folder or pressing back ends the selection.
- Not included: drag to select a range, a selection that survives changing folder.

#### `PLANNED #103`: the same browser, through the server

- A device marked "Over the internet" opens the same File browser. The request goes through
  the server.
- The top bar shows the same reach label as the card: "On your Wi-Fi" or "Over the internet".
- If the same PC is also on this network, the Wi-Fi path is used.
- If the PC drops off while the browser is open, the app says "Can't reach right now" and does
  not spin. Error meanings over the internet:

| Server answer | Say |
|---|---|
| device offline | "Can't reach right now." The PC may be off or asleep. |
| not found | You can no longer reach this, or the folder is gone. |
| subscription inactive | The owner's subscription is not active. |
| unauthorized | "That session ended. Sign in again." |

The server gives no more detail than that on purpose.

#### `PLANNED #156`: transfers that keep going in the background (Android)

- While a transfer runs and the app is not on screen, Android shows a system notification.
- The app asks for notification permission at the first transfer, not at launch, and says in
  one line what it is for.
- The desktop has no equivalent.

### 5.6 Account deletion

Not in the app. It is a website page (section 7, `NEW`). About (5.7) links to it.

### 5.8 Account `NEW` screen, `BUILT` content

Opened from the Account icon on Home. Top bar: back arrow, title "Account". It holds what the
old account card on Home held.

| State | Text | Action |
|---|---|---|
| Checking saved session | "Looking for an account on this device…" | none |
| Signed out | "Reaching your devices over the internet needs an account." | Button "Sign in" (opens 5.3) |
| Signed in | "Signed in as ana@example.com." | Text button "Sign out" |
| Signed in, server not answered | "Signed in. The server has not answered yet." | Text button "Sign out" |

Under the text, when set: the trouble line in error colour (e.g. "That session ended. Sign in
again.") and the caveat line in muted colour.

At the bottom: a row "About ProductName" that opens 5.7.

The Account icon on Home can show a small dot when there is trouble to read, e.g. the session
ended. That is the only badge in the app.

### 5.7 About `NEW`

Opened from the Account screen. Top bar: back arrow, title "About".

- App name and version, e.g. "ProductName 1.4.0"
- The update state: "You have the latest version." or the same "Version {x} is available."
  and "Download" as the Home banner
- Server in use, e.g. "productname.example". Plain text, not editable here.
- Links that open the browser: Help, Privacy, Terms, Delete my account
- "Open source licences": a list of the libraries the app ships with and their licences

Nothing else. No theme, language or notification settings.

---

## 6. PC settings page `BUILT`

A single web page served by the agent on the PC itself, at a `127.0.0.1` address. Plain HTML
with the system font, light and dark from the system, one column at most 672 px wide.
Opened by:

- `agent settings` in a terminal (all systems)
- the applications menu on Linux
- `PLANNED #159`: a macOS menu bar item with "Open settings" and "Quit"
- `PLANNED #160`: a Windows notification area icon with "Open settings" and "Quit"

The page:

1. Heading: the PC's name. Under it: "Folders on this PC that you can reach from anywhere."
2. An error line, empty unless something failed.
3. **Shared now**: one row per shared folder with its name and its path on disk, and a button
   "Stop sharing". Empty: "Nothing yet. Pick a folder below."
4. **Add a folder**: a folder walker.
   - The current path.
   - A row "↑ {parent}" to go up.
   - One row per subfolder. Tapping walks into it.
   - "Call it" field, marked optional. Without a name, the folder's own name is used, made
     lowercase with hyphens. Names: lowercase letters, digits, hyphens, 32 characters at
     most, unique on this PC.
   - Button "Share this folder". Shares the folder currently open in the walker.
5. **Account**
   - Not on an account: "This PC belongs to no account. Folders above are shared on the LAN
     either way." A "Sign in" button. The "Server" field is shown today; `NEW`: it is
     hidden behind "Use a different server", because the hosted server is built in.
   - Waiting for approval: "Approve this PC at {link}, where the code is {CODE}." The page
     also opens that link in a new tab. It checks every 2 seconds and changes when the
     person approves or refuses on the website.
   - On an account: "This PC belongs to the account at {server}, as device {device id}." A
     "Sign out" button, which takes the PC off the account.

Sharing works on the LAN whether or not the PC is on an account.

There is no file browsing, no list of connected people, no log viewer and no update button on
this page.

---

## 7. Website

Served by the same server at productname.example. All pages share one header (logo,
Download, Help) and one footer (Privacy, Terms, Delete my account). No link to GitHub or the
source code anywhere on the site.

### 7.1 Landing page `NEW`, at `/`

Its job: explain the product and send the visitor to Download. Every claim must be true of the
system as built. Sections, in order:

1. **Hero.** One line on what it does: reach the files on your home PCs from your phone or
   laptop. Button "Download". A picture of the app's Home screen from the Figma file, not
   a stock photo.
2. **How it works**, three steps:
   1. Install ProductName on each PC you want to reach, and pick the folders to share.
   2. Install the app on your phone or laptop.
   3. On the same Wi-Fi it finds your PCs by itself. Anywhere else, sign in and they are still
      there.
3. **On your Wi-Fi, or over the internet.** The same two labels the app shows on each PC.
   - On your Wi-Fi: no account, no Internet needed. The app talks to the PC directly,
     encrypted.
   - Over the internet: sign in once, add the PC to your account from the PC, and reach it
     from anywhere.
     No router setup and no port forwarding.
4. **Share a PC with someone.** Make a one-time code in the app and send it. They get access
   as a guest, and you can remove them at any time.
5. **Your files stay on your PCs.** Nothing is uploaded to a cloud drive. Files move only
   when you open, save or send one.
6. **Platforms.** PC side: Windows, macOS, Linux. App: Android, Windows, macOS, Linux.
7. **Call to action.** "Download" again.

Claims the landing page must NOT make, because they are false:

- "End-to-end encrypted" or "we cannot see your files". Away from home, the server decrypts
  and forwards each request. At home, traffic does not touch the server.
- "Works on iPhone" or "on iOS". There is no iOS app.
- Anything about price, plans or a free trial. There is no pricing section.
- "Sync", "backup" or "cloud storage". It does none of these.
- "Signed" or "verified" installers. They are not signed yet (#131).

### 7.2 Download page, at `/download`

`BUILT` today for the agent only. `NEW`: the client app joins it.

Two groups, each with the visitor's own system first:

- **Share this PC** (the agent): Windows, macOS, Linux packages. Each with its warning line
  about the unsigned installer and its next step ("then open settings and pick a folder").
- **Reach your PCs** (the client app): Android APK, Windows, macOS, Linux.
  - The Android APK has a line: Android asks to allow installs from this browser once. Link
    to the Help page for it.
  - Updates: the app tells you when a new version is here. You download it from this page
    again.

A line at the top helps a visitor pick: "On the PC with the files: Share this PC. On your phone
or laptop: Reach your PCs. A laptop can have both."

Version number in the title. The package files themselves are hosted on the release page,
which the links point at; the page does not show a "see all on GitHub" link.

### 7.3 Help `NEW`, at `/help`

One page with sections, or a small set of pages. Plain text with screenshots from the Figma
designs. Topics:

- Install on a PC: Windows, macOS (allow the unidentified developer: System Settings, Privacy
  and Security, Open Anyway), Linux
- Pick folders to share (the settings page)
- Install the app: Android APK (allow installs from the browser), desktop
- Check a PC is the real one (compare the fingerprint in the app with `agent key` on the PC)
- Reach your PCs from away (make an account, add the PC from its settings page)
- Share a PC with someone (invitation code), and remove them
- Take a PC off your account
- Update the app

### 7.4 Privacy and Terms `NEW`, at `/privacy` and `/terms`

Plain text pages. The owner writes the text. Design the layout only, with placeholder
paragraphs. What the server really stores, for whoever writes the text: email, a password
hash, sessions, the PCs on each account with their names and shared folder names, who may
reach which PC, and an audit log of those changes. File contents pass through the server only
while a remote request is in flight and are not stored.

### 7.5 Delete my account `NEW`, at `/account/delete`

- Signed out: the same sign-in form as the approve page.
- Signed in: title "Delete your account", what happens, a password field, a button "Delete my
  account" in the error colour.
- What happens, as text: you lose access to every PC on the account. The PCs keep sharing on
  their own network. Other people keep their access. A PC where you were the only admin has
  nobody left to manage it from the app, so sign it out from its settings page first if you
  want to add it to another account.
- Wrong password: "invalid password".
- Done: "Your account is deleted." and nothing else to click.

### 7.6 Account pages `BUILT`

Plain pages served by the server, one centred column, a title, an intro line, fields and one
button. After success the form is replaced by a message.

| Path | Title | Fields | Button | After success |
|---|---|---|---|---|
| `/signup` | "Create an account" | Email, Password (12 characters at least) | "Sign up" | "Check your email for a link that confirms the address, then sign in from the app." |
| `/verify` (from the email link) | "Confirm your address" | none, runs on open | none | "The address is confirmed. Sign in from the app." |
| `/reset` | "Reset your password" | Email | "Send the link" | "If there is an account for that address, a link is on its way to it." |
| `/reset/confirm` (from the email link) | "Choose a new password" | New password | "Save it" | "The password is changed and every session that was open is signed out. Sign in from the app." |
| `/approve?code=…` signed out | "Sign in to approve" | Email, Password | "Sign in" | Page reloads signed in |
| `/approve?code=…` signed in | "Approve this PC?" | none | "Approve" and "Refuse" | Done message |
| `/approve?code=…` expired or used | "Nothing to approve" | none | none | "That request expired or was already answered. Ask the PC to sign in again." |

Approve page intro, signed in: "{PC name} is asking to join {email}. Approving lets you reach
it from away. Only approve it if you just asked this PC to sign in."

Sign up always answers the same, even for an email already taken. Do not design a "this email
is taken" error.

The packages are not code signed yet (#131). The download page warns that macOS and Windows
will ask the person to allow the installer.

---

## 8. Flows

Each step names the surface. Every step is supported by the system as described.

### F1. First use on the same network, no account `BUILT`

1. Website: landing page, "Download".
2. PC: install "Share this PC" from `/download`. It starts at login and shares nothing.
3. PC: open the settings page, walk to a folder, "Share this folder".
4. Phone: download the APK from `/download` ("Reach your PCs"), allow the install, open it.
   (Android 17+: allow local network access.)
5. Phone, Home: the PC appears as "Checking…", then "On your Wi-Fi", filled
   with the folder tile. First time: the fingerprint is shown to compare.
6. Phone: tap the folder tile. File browser opens. Save, send and delete files.

### F2. Make an account and sign in to the app `BUILT`

1. App, Home: "Sign in" under the list (or Account icon, then "Sign in").
2. Sign in screen: "Create an account". The browser opens `/signup`.
3. Website: sign up. An email arrives. The link opens `/verify`.
4. App: type server, email, password, "Sign in". Home shows "Signed in as …".

### F3. Add a PC to the account `BUILT`

1. PC, settings page, Account: type the server address, "Sign in".
2. The page shows a code and opens `/approve` in a new tab.
3. Website: sign in if needed, then "Approve".
4. Settings page changes to "This PC belongs to the account at …".
5. App, Home: after Refresh the PC shows "You manage it". Away from the Wi-Fi it says
   "Over the internet".

A PC with no screen: the agent prints the link and code in the terminal. The person opens the
link on their phone's browser and approves there.

### F4. Invite someone `BUILT`

1. Admin, app, Home: "Manage" on the PC's card. Manage device opens.
2. Choose "Guest" and "1 day", "Make a code", "Share". Send it by any app.
3. Guest, app: sign in (make an account first if needed, F2).
4. Guest, Home: paste the code in "Got a code from someone?", "Join".
5. The PC appears on the guest's list with "Guest" and "Over the internet".

### F5. Remove someone `BUILT`

1. Admin, Manage device: "Remove" next to the person, confirm.
2. Their next request through the server fails. The PC leaves their list at the next refresh.

### F6. Reach files from outside the house `PLANNED #103`

1. Not on the same Wi-Fi, signed in: Home shows the PC as "Over the internet".
2. Tap a shared folder on that card. The File browser opens, marked "Remote".
3. If the PC is off: the card says "Can't reach right now" and its tiles are disabled.

### F7. Session ended somewhere else `BUILT`

1. The password was reset on the website, which signs out every session.
2. App start: the Account icon gets a dot. The Account screen shows "That session ended.
   Sign in again." and a "Sign in" button. Devices on the Wi-Fi keep working.

### F8. Take a PC off the account `BUILT`

1. PC, settings page: "Sign out".
2. The PC keeps sharing on its own network. It no longer shows "Over the internet" for
   anyone, and leaves the list of people who are not on its network.

### F9. A PC that is not what it claims `BUILT`

1. Something on the network advertises a known PC's identity but cannot prove it.
2. Home: that card turns to the error colour with a warning icon and the reason. It has no
   folder tiles and cannot be opened.

### F10. Update the app `NEW`

1. App, Home: banner "Version {x} is available.", "Download".
2. Browser: `/download` opens. Download the new APK or desktop app.
3. Install over the old one. Sign-in and known PCs are kept.

### F11. Delete the account `NEW`

1. App, About: "Delete my account". The browser opens `/account/delete`.
2. Website: sign in if needed, type the password, "Delete my account".
3. App: at the next start the Account screen shows "That session ended. Sign in again."

---

## 9. Not supported: do not design

- Sign up, password reset, or email change forms inside the app
- Sharing a folder, or adding a PC to an account, from the app
- Changing what a PC shares from anywhere except its settings page
- Thumbnails, image or video preview, a media player, a text viewer
- Rename, move, copy, create folder, zip or unzip
- Share links, public links, links that work without the app
- Upload or download progress in percent, speed or time left (a plain bar only; "3 of 12"
  only with #165)
- Pause, resume, or a transfer queue
- Sorting, filtering, grid view
- Favourites, recent files, offline copies, sync, backup
- Storage used, disk space, quotas
- Device details: OS, model, battery, last seen, IP change history
- Profile pictures, display names, a profile screen
- Changing someone's role (remove and invite again instead)
- A list of open invitations, or cancelling one
- Push notifications (only Android's own transfer notification, #156)
- Chat, comments, activity feed, audit log view
- Billing, plans, payment, upgrade prompts
- Two-factor sign in, social sign in, passkeys
- Settings screen in the app (theme, language, and so on)
- A web dashboard for devices or people
- Onboarding carousels with claims the product cannot back
- An iOS or iPhone app, or App Store and Google Play badges
- Pricing, plans, trials, or "upgrade" anywhere
- A link to GitHub or the source code on the website
- Automatic updates inside the app
- A server address a person must type (it is built in, advanced only)

---

## 10. Platform and layout notes

- Phone: design at 360 × 800 and 412 × 915. Edge to edge: content sits below the status bar,
  and nothing sits under the gesture bar.
- Desktop: a resizable window. Design at 1024 × 720 and 1440 × 900. The same screens as the
  phone, with the same one-column flow. A wider layout is fine if it shows the same content
  and actions.
- Desktop has no system back. The back arrow in the top bar is the only way back.
- Android back gesture: goes up a folder, then to Home.
- Material 3 components throughout: cards, list items, filter chips, top app bar, extended
  floating action button, snackbar, dialog, linear and circular progress indicators.
- Both light and dark. Android 12+ uses dynamic colour from the wallpaper, so do not rely on
  one fixed brand colour for meaning. Use the colour roles: primary container for "ready",
  error container for "refused".
- Text can be long: device names, file names, emails, paths. Show how each truncates.

---

## 11. Source references

- Architecture: `docs/new-arch.md`
- Decisions: `docs/adr/0005-go-agent-native-tunnel-opaque-sessions.md`,
  `docs/adr/0006-agent-configures-itself-browser-enrolment.md`
- Client screens: `client/common/src/commonMain/kotlin/io/anywherefile/client/` (`Home.kt`,
  `SignIn.kt`, `Remote.kt`, `FileBrowser.kt`), Android gate in
  `client/androidApp/src/main/kotlin/io/anywherefile/client/MainActivity.kt`
- Server API as recorded: `client/control-plane.http`, `client/dufs.http`
- PC settings page: `device/agent/settings.html`
- Website: `hosted/control-plane/web.go`, `web.html`, `download.html`
- Open issues: #103, #156, #163, #164, #165 (client), #159, #160 (PC menu bar and tray)

---

## 12. Decisions of 2026-09-28, and the issues they need

Decided with the product owner:

- One hosted server at productname.example, built into the app and the agent.
- No app store. APK and desktop apps from the website.
- The agent and the client are separate downloads on a desktop.
- No pricing shown anywhere. No GitHub link on the website.
- Placeholder name ProductName until the name is chosen.
- Home is one device list. Each card says how the app reaches it: "On your Wi-Fi", "Over the
  internet", "Can't reach right now" or "Checking…". The account moves to its own screen
  behind an icon, and joining with a code is a dialog. #103 is rewritten to match.
- The website gains one account task, deleting the account. ADR 0006 says the website has no
  account management, so it needs a line of amendment.

Filed on 2026-09-28. Each is tagged `NEW` above.

| Area | Issue | Number |
|---|---|---|
| setup | Build and publish the client on a version tag: Android APK and desktop apps for Windows, macOS, Linux | #172 |
| cloud | Deploy the hosted server with HTTPS at the real domain | #173 |
| client and agent | Build in the hosted server address, and hide the Server field behind "Use a different server" | #175 |
| client | Show the fingerprint on first contact (#162 asked for it) | #176 |
| client | Update notice on Home and in About, from the website's latest version | #177 |
| client | About screen | #178 |
| client | Home as one device list with reach labels, an Account screen, and the code dialog | #174 |
| website | Landing page | #179 |
| website | The client in the download page, in two groups | #180 |
| website | Help pages | #181 |
| website | Privacy and Terms pages | #182 |
| website | Delete my account page | #183 |

Found later the same day by walking every flow in `docs/flows.drawio`. The screens these add
are not described above yet; design them from the issues.

| Area | Issue | Number |
|---|---|---|
| client | A PC that speaks another version shows, with which side to update | #184 |
| agent | The settings page opens when the install finishes | #185 |
| website | The approve page links to sign up and password reset, and comes back | #186 |
| client | An invitation message that works for someone new | #187 |
| cloud and client | A guest leaves a PC ("Leave this PC" on the card) | #188 |
| cloud, client and agent | An admin removes a PC they no longer have ("Remove from my account") | #189 |
| agent | The settings page shows its version and says when a newer one is out | #190 |

#142, #159 and #160 (the macOS menu bar item and the Windows tray icon) are P1.
