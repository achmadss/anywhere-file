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
4. The owner of a PC can **invite** someone else with a one-time link. That person becomes a
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

**Two separate downloads on a desktop.** "Share this PC" is the agent. "Reach your devices" is
the client app. A person may need one or both. They are separate installers.

---

## 3. Words used in the UI

Keep these words. They are already in the app's copy.

| Word | Meaning |
|---|---|
| device | A computer running the agent. The app always says "device", never "PC". Only the computer's own settings page and the "Share this PC" installer say "this PC". |
| shared folder | One folder a PC shares. Technically an "application" called by a short name, e.g. `files`. |
| "On your Wi-Fi" | The app reaches this device directly, on the same Wi-Fi or network. No account needed. |
| "Over the internet" | The app reaches this device through the ProductName server. Needs an account. |
| "Can't reach right now" | Neither path works: the PC is off, asleep, or not connected. |
| "Checking…" | The app is still finding out. |
| account | Email and password on our server. |
| admin | The one account signed in on the PC's settings page. A PC has exactly one. May see who can reach the PC, remove people, invite people and remove the PC. Its PCs show under "Your devices". |
| guest | Everyone else who can reach the PC, through an invite link. May reach the PC's folders and leave. Its PCs show under "Shared with you". |
| invite link, "link" | A one-time web address an admin makes and sends to someone, `https://productname.example/join#<code>`. It always makes a guest. The app says "link", never "code". |
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
| fingerprint | `SHA256:q3vX...` (43 characters after the prefix) | Always derivable. Shown behind "Show code" in the device sheet (#176). |

There is no OS, no icon, no disk size, no "last seen" time, no owner name.

### 4.2 A device on the account (client, `RemoteDevice`)

| Field | Example |
|---|---|
| name | `pc1` |
| online | yes / no |
| role | admin or guest |
| shared folders | `files` |
| owner | `bob@example.com`, the admin's email. Guest devices only. `NEW` |

The list is fetched when the home screen appears and when the person taps Refresh. It does
not update live.

### 4.3 A person on a device (admin view, `DeviceUser`)

| Field | Example |
|---|---|
| email | `ewa@example.com` |
| role | admin or guest |
| folders | `files`, `photos`. The shared folders a guest can open. The admin opens all of them. `NEW` |

No display name, no avatar, no "last active", no "invited by".

### 4.4 An invitation

| Field | Example |
|---|---|
| code | `BOBIEyhfUSwH8MUL8nl42HMQKuAmgoWlMdKlKrRvIZ0` (43 characters) |
| role | guest. The server still accepts admin today; that goes (#191). |
| until | a date and time, or none. `NEW` (#193) |
| note | "For Ewa", optional, seen only by the admin. `NEW` (#193) |
| folders | `files`, `photos`. The shared folders the guest can open, at least one. `NEW` |

The admin can open an unused link again and copy it at any time. So the server must keep the
code in a form it can give back to the admin. A hash alone is not enough (#193). `NEW` (#193):
no end date, the note, the list of unused links, seeing a link again, and removing one.

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
| plan | Premium or Free. Known once the server answered. `NEW` |
| signed in | yes / no |
| session | `NEW`: stays signed in until the person signs out. The app renews the sign-in in the background with a refresh token, and for now that token never runs out. There is no "checking" or "session ended" screen. If the server refuses the token (password reset, account deleted), the app is simply signed out. |
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
Home ──► Device (NEW) ──► File browser ──► (Search, PLANNED #164)
  │
  ├──► Device sheet (NEW) ──► Manage device (admins only)
  │
  └──► Account (NEW) ──► Sign in ──► Create an account (NEW)
                     └──► About (NEW)

First launch only: Onboarding (5.0) ──► Home
```

### 5.0 Onboarding `NEW`

Shown on the first launch only. Later launches open Home. Built like TachiyomiSY's onboarding
(Figma page "Akdes New", row 01). One screen, no steps:

- At the top, a 48 px icon in the primary colour, the heading "Welcome to ProductName" and the
  line "Set up this phone to open the files on your devices."
- Then two rounded boxes (surface container high), each with a title, a line, then full-width
  buttons at the bottom of the box. Nearby devices is needed and the account is not, so they
  don't share a box.
  1. "Find nearby devices", Android 17 and later only (5.1). Older Android hides this box.
  2. "Account (optional)", then "Sign in to open your devices when you are away from home. On
     your own Wi-Fi you don't need an account.", a tonal "Sign in" (opens 5.3) and an outlined
     "Create an account" (opens 5.3.1).
- At the bottom, above a thin line, one full-width filled button "Get started" that opens
  Home, signed in or not, allowed or not. If nearby devices are not allowed, Home shows the
  same box in its Nearby section (5.2), so the ask is not lost.
- When sign in or create an account works and nearby devices are allowed, the app goes
  straight to Home, with no signed-in step.
- No step dots and no Skip.

Setting up the device that has the files is not in onboarding: it happens on that device.
Its steps are on the Help page (7.3), reached from "How to set up your device" on the empty Home (5.2).

### 5.1 Nearby devices permission (Android 17 and later only) `BUILT`, `NEW` placement

The first box of onboarding (5.0):

- Title: "Find nearby devices"
- Line: "Starting with Android 17, permission to find nearby devices is needed."
- Buttons, full width at the bottom of the box, like the account box:
  - Not allowed yet: a tonal "Allow". It opens Android's own prompt. No "Not now": Android's
    prompt already lets the person say no.
  - Refused twice: Android stops showing its prompt, so "Allow" would do nothing. The box shows
    a tonal "Open settings", which opens the app's page in Android's settings, and an outlined
    "How to allow permission" with the help icon, which opens the browser at the Help page (7.3),
    section "Allow nearby devices".
  - Allowed: no buttons. A check in the primary colour sits at the end of the title.
- "Get started" always works. The same box shows on Home until it is allowed (5.2).

### 5.2 Home `NEW` layout, built on `BUILT` data

One list of devices. There are no "On this network" and "Away from home" sections. Each
device appears once, and its card says how the app reaches it right now.

1. Title row: the product name, then the Account icon (opens 5.8). No Refresh icon on the
   phone: pull the list down to refresh. The standard Android spinner, in a small raised circle,
   drops in under the top bar and the list moves down with it. Home asks the account and
   searches the Wi-Fi again, and the circle goes when both answer. It works in every Home state,
   empty ones too. The desktop keeps a Refresh icon.
2. Device cards in three groups, or one of the empty states:
   - "Your devices": PCs this account is the admin of.
   - "Shared with you": PCs this account is a guest on.
   - "Nearby": PCs found on the Wi-Fi that are not on this account.
3. `NEW` (#187): no join button. A guest joins a PC from the invite link in a browser (7.7),
   and the PC shows under "Shared with you" at the next refresh.

`NEW`: Home has no update notice and no sign-in notice. Updates live in About (5.7), and
signing in lives in Account (5.8). Home stays about devices.

`NEW`: Home has no notice card or banner at the top. Two problems can be true at once (nearby
devices off and server down), and stacked banners push the list down and compete. Each
problem shows inside the section it affects (below), so both can show at once and stay clear.

#### 5.2.1 How the list is built

Two sources, joined by the device ID (the same PC has the same ID in both):

- Devices found on the same Wi-Fi or network. Live: they appear and go as PCs join and
  leave. Works signed out.
- Devices on the account, from the server. Signed in only. Fetched when Home opens and on
  Refresh. Not live.

A device found on the Wi-Fi that is not on the account still shows, under "Nearby".

Inside each group: "On your Wi-Fi" first, then "Over the internet", then "Can't reach right
now". By name inside each.

#### 5.2.2 Device card

Top of the card: computer icon, name, and the reach label with a small dot.

| Reach label | When | Dot | Folders open? |
|---|---|---|---|
| "On your Wi-Fi" | Found on the network and it proved who it is. The app talks to it directly. Wins when both paths work. | filled | Yes, directly `BUILT` |
| "Over the internet" | On the account, the server says it is connected, and it is not on this network. | filled | Yes, through the server `PLANNED #103` |
| "Can't reach right now" | On the account, not connected to the server, and not on this network. | hollow | No. The device screen opens, with its folder rows disabled. |
| "Checking…" | Just found on the network and still being checked, or the server list is still loading. | dotted | Not yet |

A separate error state, whatever the path:

| State | Look | Text |
|---|---|---|
| Refused | Error colour, warning icon instead of the computer icon. It can't be opened. | "Can't confirm it's this device" and under it "Another device on this Wi-Fi uses this name. To keep your files safe, ProductName won't connect to it." |

`NEW`: the card holds only the icon, the name, the reach label and the three dots. No folders,
no role line, no address. The group heading says whose PC it is. A card under "Shared with you"
adds a third line, "Shared by bob@example.com" in muted colour, under the reach label, so the
guest knows whose device it is. A short card keeps Home
readable with many devices, and a device with many folders doesn't push the others down.

Tapping a card opens the device screen:

- Top bar: back arrow, the device name as the title, the reach label under it, and the three
  dots on the right (the same sheet as on Home).
- One list row per shared folder: folder icon, name and a chevron, lined up under the back
  arrow. Tapping a row opens the File browser. A long list scrolls. "Nothing shared yet" when
  there are none.

The three dots open a bottom sheet (`NEW`, in place of a dropdown menu):

- At the top, the same device card, without the three dots, so the person sees which device
  they act on.
- Then list rows with leading icons:
  - Admin: "Show code", "Manage access" (opens 5.4) and "Remove".
  - Guest: "Show code" and "Remove".
  - A nearby device that is not on the account: "Show code" only.
- The sheet has room for more device actions later.
- Admin "Remove" asks first, in a dialog: "Remove pc1 for everyone?", "Everyone who can reach
  pc1 loses access, including your 2 guests. If pc1 comes back online, it signs out. To add it
  again, sign in on its settings page.", buttons "Cancel" and "Remove for everyone" (#189). It
  is on every admin card: a PC that can't be reached may only be switched off, so the app
  can't tell a gone PC apart.
- Guest "Remove" asks first: "Remove office-pc?", "You won't be able to open its folders. To
  come back, you need a new link from the person who shared it.", buttons "Cancel" and
  "Remove" (#188). The menu says "Remove" in both cases, and the dialog says what happens.

Colour: every card is a filled card with the same quiet surface (surface container highest,
12 px corners). Only the icon circle and the reach label
change colour, and the refused card uses the error colour, so a problem stands out. The
label and the dot say the same thing as the colour, so colour is never the only signal.

`NEW` Show code (#176): "Show code" in the sheet opens a second bottom sheet, "Compare codes",
with the fingerprint in large grouped text and "Open ProductName on that device. It shows a
code under "This device". If it is not the same as the code below, don't open files on it."
The sheet has no buttons; swipe down or go back to close it. The card shows no first-contact
line: most people never check the code, so it waits in the sheet until someone wants it.

Empty states:

- First 5 seconds, nothing found yet: a centred spinner and "Looking for your devices…". Home
  asks the account and searches the Wi-Fi at the same time, so the line names neither.
- `NEW` After 5 seconds, nothing on the account and nearby devices not allowed: the "Nearby"
  header and the nearby devices box (5.1) are the whole screen, with no "No devices found yet".
  Allowing is the only way to find anything. Once allowed, Home looks again.
- After 5 seconds, nothing on the account, nearby devices allowed, and nothing on the Wi-Fi,
  centred: a computer icon, title "No devices found yet", text "Open ProductName on your
  device. It shows up here when it's on this Wi-Fi or signed in to your account." (every line
  centred), a text button "How to set up your device"
  with the help icon. It opens the Help page (7.3) at "Set up your device" in the browser. Those
  steps include getting ProductName for the device, so one button covers both. The top bar has
  only the Account icon.
- `NEW` Server did not answer: no banner. The account's devices stay listed from the last
  answer, and each card says "Can't reach right now". Pull down to try again. Devices on the
  Wi-Fi still work.
- `NEW` Nearby devices not allowed (Android 17 and later, skipped in onboarding or turned off
  later in Android's settings): the "Nearby" section always shows, and holds the same box as
  onboarding (5.1) in place of devices: "Find nearby devices", the Android 17 line and "Allow".
  After two refusals: "Open settings" and "How to allow permission" (opens the browser at the
  Help page, "Allow nearby devices"). "Your devices" and "Shared with you" still show above it.
  Once allowed, the box goes and devices on the Wi-Fi fill the section.
- Devices on the account and none on the Wi-Fi (allowed): not empty. The list shows them and
  the "Nearby" section is hidden.

#### 5.2.3 Joining a device `NEW` (#187)

The app has no join screen. Joining happens on the website (7.7), in any browser, with or
without the app. The `BUILT` "Join with a code" dialog goes.

### 5.3 Sign in `BUILT`

Top bar: back arrow, title "Sign in".

- Text: "Sign in to reach your devices from anywhere. On your own Wi-Fi you don't need an
  account."
- Field "Server", a web address. Pre-filled with the last one used. `NEW`: hidden by default,
  because the hosted server is built in. A text button "Use a different server" at the bottom
  shows it, with the line "Only if you run your own server." under it. The button then reads
  "Use the ProductName server" and hides the field again. Most people never see it.
- Field "Email"
- Field "Password", hidden, with an eye icon that shows it
- Progress bar while signing in. The fields and the button are greyed out.
- Error line under the password field, for example:
  - "The email or password is wrong."
  - "Type the address of your server first."
  - "The address has to start with http:// or https://."
  - "Only the address goes here, with nothing after it."
- Button "Sign in", full width. Disabled until email and password are filled.
- `NEW`: an outlined button "Create an account", full width, right under "Sign in". It opens
  5.3.1 in the app.
- Text button "Forgot your password?", under "Create an account", with the open-in-new icon.
  Opens the website's reset page in the system browser.

On success the screen closes and the person is back where they came from (Home or
onboarding).

There is no password reset form, no "remember me", no social login and no two-factor step in
the app.

#### 5.3.1 Create an account `NEW`

Top bar: back arrow, title "Create an account".

- Text: "Make an account to reach your devices from anywhere."
- Fields "Email" and "Password" (with the eye icon), the same outlined fields as Sign in.
- Button "Create account", full width. The same progress bar and greyed-out fields while it
  works.
- Errors under the field they belong to, e.g. "An account with this email already exists."
  (only for an active account) or "Use at least 12 characters."

"Create account" makes the account at once. The server stores the email and the password
hash with the account marked as not active yet, and emails a 6 digit code to that address.
The code screen opens.

Code screen: top bar "Check your email".

- Text: "We sent a code to ana@example.com. Type it here to finish making your account."
- Field "Code", the same outlined field.
- Button "Confirm", full width.
- Text button "Resend code". It emails a new code, and the old one stops working. After each
  email it is greyed out for 60 seconds with a countdown, "Resend code in 0:42". The server
  refuses a resend inside those 60 seconds too, so the limit holds without the app.
- A wrong or old code: "That code doesn't work." under the field.

A right code turns the account active and signs the app in. All the screens close and the
person is back where they came from.

An account that is not active can't sign in or be invited. Signing in to it with the right
password opens the code screen, with a new code sent. "Create account" again with the same
email while it is not active replaces the password and sends a new code.

### 5.4 Manage device (admins only) `BUILT`

Top bar: back arrow, the title "Manage access" and the device name under it.

Progress bar and error line at the top when needed.

`NEW` (#187): invite links move to their own screen. Manage access holds, from the top:

- A list row "Invite links" with a link icon and "{n} not used yet" under it ("None yet" when
  there are none). It opens the Invite links screen.
- A divider, then the list "Who can reach pc1" under a section header in the primary colour.

**Who can reach pc1**

One row per person:

- Email
- The admin's row says "You". Everyone else is a guest and has a text button "Remove".
- `NEW`: a guest's row says "Can open: files, photos" under the email. Tapping the row opens
  the "Change folders" dialog.

Remove opens a dialog:

- Title: "Remove ewa@example.com?"
- Text: "They lose access to pc1 straight away. A new link brings them back."
- Buttons: "Cancel", "Remove"

`NEW`: "Change folders" is the "Choose folders" dialog from Invite links, with other words:

- Title: "Change folders"
- Text: "ewa@example.com can open only the folders you tick."
- The same scrolling list of the device's shared folders. The guest's folders are ticked.
- Buttons: "Cancel" and "Save". "Save" is greyed out when nothing is ticked. To take all
  access away, the admin uses "Remove".

Save takes effect at once. A folder they lose leaves their device screen at the next refresh,
and their next request for it gets "not found". A folder the device starts sharing later is
not given to guests. The admin ticks it for each guest who needs it.

The admin cannot remove themselves here. To give the PC to someone else, sign out on the PC's
settings page and let them sign in there.

**Invite links** `NEW` (#187, #193), its own screen

Top bar: back arrow, the title "Invite links" and the device name under it. An extended
floating button "Create" with a plus icon at the bottom right.

One row per unused link: the link on one line, cut in the middle with "…" so its start and end
both show, then "Expires at {date}". Two icon buttons on the right:

- Copy: copies the link, and a snackbar says "Link copied".
- Trash: asks first. "Remove this link?", "Nobody can join with it after this.", buttons
  "Cancel" and "Remove".

Tapping the row opens the link dialog.

With no unused links, the middle of the screen shows a link icon, the heading "No invite links
yet" and "Create a link and send it to the person you want to invite. It works for 7 days."
There is no button in it, because "Create" is already at the bottom right.

`NEW`: "Create" opens the "Choose folders" dialog:

- Title: "Choose folders"
- Text: "They can open only the folders you tick."
- One row per shared folder of the device, each with a tick box. Nothing is ticked at first.
  The list scrolls inside the dialog, between two thin lines, so the buttons stay in view.
- Buttons: "Cancel" and "Create link". "Create link" is greyed out until a folder is ticked.

"Create link" makes the link for the ticked folders. For now the app always asks for a link
that expires in 7 days, with no note. The server still accepts other durations. The new link
goes to the top of the list, and the link dialog opens.

**Invite link** `NEW` (#187, #193), a dialog

Opens after "Create link", and again when the admin taps a link row.

- Title: "Invite link"
- Text: "Expires at {date}."
- Under it: "Can open: files, photos", the folders the link gives. `NEW`
- The link in monospace, selectable, in a box of the highest surface container colour
- Buttons: "Copy link" on the left, "Remove" on the right in the error colour

Copy link copies the link, and a snackbar says "Link copied". The dialog stays open. Remove asks
first, with the trash dialog above. Tapping outside or Back closes the dialog.

`SERVER ONLY`: disabling a device. The server has it, there is no way to undo it, and no issue
asks for a screen. Do not design it.

### 5.5 File browser `BUILT`, with additions from `PLANNED #163 #164 #165 #103 #156`

Opened from a folder row on the device screen. Shows one shared folder of one device.
Figma: page "Akdes New", row "06 Phone · File browser".

#### Built today

Top bar:

- Back arrow. In a subfolder it goes up one folder. At the top it returns to Home.
- Title: the shared folder's name. Under it, in small text, the current path when not at the
  top, e.g. `holiday/2026`.
- Refresh icon. `NEW`: removed. Pulling the list down loads it again, as on Home. The only
  icon is search (#164).

Body states:

| State | Look |
|---|---|
| Loading | Centred spinner |
| Failed | A laptop icon, "Can't reach right now", "pc1 may be off or asleep." and a button "Try again". The same screen on the Wi-Fi and over the internet. Other errors ("That folder is not on the device any more.") use the same layout with their own text |
| Empty at top | "This folder is empty." |
| Empty below top | "Nothing in holiday/2026." |
| Listing | One row per entry, divider between rows |

Rows:

- Folder: name, modified date, chevron. Tapping opens it.
- File: name (long names shortened in the middle), "2.4 MB · 3 Sep 2026", and a three-dot
  menu. Tapping the row saves the file.
  - Menu item "Save a copy here"
  - Menu item "Delete from the device". Only when the folder allows delete.

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

#### `NEW`: file actions

Every row, folder or file, has a three-dot button. Tapping a folder opens it. Tapping a file
opens it in its app, the same as "Open with".

The three dots open a bottom sheet. On top, the same row without the dots, on a rounded
tinted box. Then one row per action, each with a leading icon:

- "Open with": the file is fetched, then Android's app chooser opens. Files only.
- "Save to Downloads": the old tap on a row. Files only.
- "Share": the file is fetched, then Android's share sheet opens. Files only.
- "Cut" and "Copy": a bar sits at the bottom, "Moving 1 file" or "Copying 1 file", with × on
  the left and a tonal "Paste here" button on the right. It stays while the person opens
  another folder in the same shared folder. "Paste here" moves or copies it there. × or Back
  cancels. Pasting into another shared folder is not offered.
- "Rename": a dialog "Rename" with an outlined field "Name", the name filled in. "Rename" is
  greyed out until the name changes. A name that is taken shows an error under the field.
- "Compress": the same dialog, titled "Compress", the name filled in as `beach.zip`, button
  "Compress". The zip is made on the device, in the same folder.
- "Properties": a dialog "Properties" with read-only pairs: Name, Where (`files/holiday/2026
  on pc1`), Type, Size, Modified. One button, "Close".
- A thin line, then "Delete" in the error colour. It asks first: "Delete beach.jpg?", "It is
  removed from pc1. You can't undo this.", buttons "Cancel" and "Delete".

Actions that change the device (Cut, Rename, Compress, Delete) show only when the folder
allows delete and upload. Copy and Paste need upload.

The floating button is a square "+" (it was "Send a file"). Tapping it shows two labelled
buttons above it, "Send a file" and "New folder", over a scrim, and the "+" turns into "×".
"New folder" opens the Rename dialog titled "New folder", field "New folder", button
"Create", greyed out while the name is empty or taken. The "+" shows only when the folder
allows upload.

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
- A result row: the name, and the folder that holds it. With "whole device", also the shared
  folder it came from.
- Tapping a file saves it. Tapping a folder closes search and opens the File browser in that
  folder.
- Matching ignores upper and lower case. Folders are results too.
- States: empty field, searching, no results, error.

#### `PLANNED #165`: select several files

- A long press on a row starts a selection. Then a tap adds or removes a row.
- The top bar becomes a selection bar: a close button, "3 selected" and a select all icon. When every item is picked, it turns into
  a deselect all icon. `NEW`: the
  actions move to a bar at the bottom, as in TachiyomiSY: cut, copy, save, delete, and three
  dots for compress and share.
- The bar shows only actions that work on every picked item. It holds 5 icons. With more than
  5, the first 4 stay and the three dots open a menu with the rest. A folder in the selection
  removes save and share (files only), which leaves 4 icons and no three dots. With one item,
  the menu also has open with, rename and properties. Actions the folder does not allow are
  left out.
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
| unauthorized | The app is signed out (F7). The browser goes back to the device screen. |

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
| Signed out | Title "Not signed in", then "On your own Wi-Fi you don't need an account. To reach your devices from anywhere, sign in." | Button "Sign in", full width (opens 5.3) |
| Signed in | The email as the title, a small tonal label with the plan ("Premium" or "Free"), then "Signed in. You can reach your devices from anywhere." | Row "Sign out" |
| Signed in, server not answered | The email as the title, then "Signed in. The server has not answered yet." | Row "Sign out" |

`NEW`: no "checking your account" and no "session ended" state. A sign-in lasts until the
person signs out (4.6).

Under the text, when set: the caveat line in muted colour.

Under that: a row "About ProductName", one line with no summary, that opens 5.7, then the
"Sign out" row with the line "Devices on your Wi-Fi keep working" when signed in. Each row has
a leading icon in the primary colour, as in
TachiyomiSY's More screen. Sign out has no confirm step: signing in again undoes it.

The Account icon on Home has no badge.

### 5.7 About `NEW`

Opened from the Account screen. Top bar: back arrow, title "About". A plain list with no
header and no leading icons:

- "Check for updates", with the version under it, e.g. "Version 1.4.0". Tapping it:
  - While it checks, the line says "Checking…" and a small circular spinner sits at the end of
    the row, where the other rows have their icon.
  - A newer version: a dialog "Version 1.5.0 is out", "You have 1.4.0. Download the new
    version from productname.example.", text buttons "Later" on the left and "Download" on the
    right. Download opens the website's download page. Later closes the dialog.
  - No newer version: the line says "You have the latest version".
  - The check fails: the line says "Couldn't check. Try again later".
  The app checks only when asked. No automatic install.
- Server in use, e.g. "productname.example". Plain text, not editable here.
- Links that open the browser, each with an "opens outside" icon: Help, Privacy, Terms, Delete
  my account
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
4. **Share a PC with someone.** Make a one-time invite link in the app and send it. They get access
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
- **Reach your devices** (the client app): Android APK, Windows, macOS, Linux.
  - The Android APK has a line: Android asks to allow installs from this browser once. Link
    to the Help page for it.
  - Updates: the app tells you when a new version is here. You download it from this page
    again.

A line at the top helps a visitor pick: "On the PC with the files: Share this PC. On your phone
or laptop: Reach your devices. A laptop can have both."

Version number in the title. The package files themselves are hosted on the release page,
which the links point at; the page does not show a "see all on GitHub" link.

### 7.3 Help `NEW`, at `/help`

One page with sections, or a small set of pages. Plain text with screenshots from the Figma
designs. Topics:

- `NEW` Set up your device, moved here from the app's onboarding: "On the device that has
  your files: 1. Go to productname.example/download 2. Install "Share this PC" 3. Open it and
  pick the folders to share". "How to set up your device" on the app's empty Home opens this section.
- `NEW` Allow nearby devices (Android 17 and later): where the permission is in Android's
  settings, with screenshots. "How to allow permission" in the app opens this section in the browser.
- Install on a PC: Windows, macOS (allow the unidentified developer: System Settings, Privacy
  and Security, Open Anyway), Linux
- Pick folders to share (the settings page)
- Install the app: Android APK (allow installs from the browser), desktop
- Check a PC is the real one (compare the fingerprint in the app with `agent key` on the PC)
- Reach your devices from away (make an account, add the PC from its settings page)
- Share a PC with someone (invite link), and remove them
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

`NEW`: sign up uses a code on the website too, the same way as the app (5.3.1). `/signup`
makes the account, not active yet, and shows a code field with "Confirm" and "Resend
code" (with the same 60 second wait) in place of "Check your email for a link". The `/verify` link goes. An email that
already has an active account gets "An account with this email already exists.", as in the
app.

The packages are not code signed yet (#131). The download page warns that macOS and Windows
will ask the person to allow the installer.

---

### 7.7 Join page `NEW`, at `/join` (#194)

An invite link opens this page in the browser. The whole join happens here, so it works on a
desktop or a phone with no app at all. The code sits after the `#`, so the browser never sends
it in the address. The page's script reads it and asks the server about it.

The page follows the website's design system. The Figma row "05 Browser · Join from an invite
link" is a reference for content and flow only; the website's own parts, colours and type win.

Same layout as the account pages (7.6): one column, a title, a line, then the form or
buttons. The main button sits right under the fields or the text, never pinned to the bottom,
because the page scrolls and a phone keyboard covers the bottom. `NEW`: that block sits in the
middle of the page, top to bottom, on every state.

Signed out, the page shows nothing about the invite: no device name and no inviter. It can't
know yet that the link was meant for this person. Signed in, the block has an invite card: a
rounded card with the device icon, the device name ("pc1") and "Invited by ana@example.com".
It sits under the title and the guest line, and above "Signed in as ewa@example.com.", so the
account that accepts sits right above the button. Joined and "doesn't work" start with a large check
or error icon tile.

| State | Title | Text | Buttons |
|---|---|---|---|
| Signed out | "Sign in", the same words as every other sign-in | "Sign in to reach your devices from anywhere. On your own Wi-Fi you don't need an account." Fields Email and Password. | "Sign in", then text buttons "Forgot your password?" and "Create an account". Both go to their page and come back to this link (as #186 does for the approve page). |
| Signed in | "You have been invited" | "You can open pc1's shared folders as a guest. Only ana@example.com can manage it." and "Signed in as ewa@example.com." | "Accept invite", and a text button "Use another account" that signs out and shows the signed-out state |
| Accepted | "Invite accepted" | "pc1 is under "Shared with you" in ProductName, on any device where you sign in as ewa@example.com." | "Open ProductName" (opens the app on Home through an app link; without the app it lands on the download page), text button "Get the app" |
| Link doesn't work | "This invite link doesn't work" | "It was used, cancelled or is out of date. Ask the person who sent it for a new one." | none |

A wrong password shows "The email or password is wrong." under the password field, as on the
approve page.

## 8. Flows

Each step names the surface. Every step is supported by the system as described.

### F1. First use on the same network, no account `BUILT`

1. Website: landing page, "Download".
2. PC: install "Share this PC" from `/download`. It starts at login and shares nothing.
3. PC: open the settings page, walk to a folder, "Share this folder".
4. Phone: download the APK from `/download` ("Reach your devices"), allow the install, open it.
   Onboarding: allow nearby devices (Android 17+), then "Get started". Setup help: "How to set up
   your device" on the empty Home.
5. Phone, Home: the PC appears as "Checking…", then "On your Wi-Fi". To compare codes: three
   dots, "Show code".
6. Phone: tap the card, then the folder row. File browser opens. Save, send and delete files.

### F2. Make an account in the app `NEW`

1. App: onboarding step 2 or the Account screen, then "Create an account" (from Account, it
   sits under "Sign in" on the Sign in screen).
2. Create an account: type email and password, "Create account". The account exists, not
   active yet, and an email with a code arrives.
3. Code screen: type the code, "Confirm". The account is active, the app is signed in
   and goes back where it came from. The Account screen shows the email. No code: "Resend
   code".

### F3. Add a PC to the account `BUILT`

1. PC, settings page, Account: type the server address, "Sign in".
2. The page shows a code and opens `/approve` in a new tab.
3. Website: sign in if needed, then "Approve".
4. Settings page changes to "This PC belongs to the account at …".
5. App, Home: after a refresh the PC shows under "Your devices". Away from the Wi-Fi it says
   "Over the internet".

A PC with no screen: the agent prints the link and code in the terminal. The person opens the
link on their phone's browser and approves there.

### F4. Invite someone `BUILT`, `NEW` as a link (#187, #193, #194)

1. Admin, app, Home: three dots on the card, "Manage access". Manage device opens.
2. "Invite links", then "Create". In the link dialog, "Copy link". Paste it into any app. Later, tap the link's row to see it
   and copy it again.
3. Guest opens the link in any browser. The join page (7.7) asks them to sign in if needed
   (or make an account and come back), then shows "You have been invited".
4. "Accept invite". The page says "Invite accepted" and offers "Open ProductName". In the app,
   the PC is under "Shared with you", "Over the internet".
5. The link leaves the admin's Invite links screen.

### F5. Remove someone `BUILT`

1. Admin, Manage device: "Remove" next to the person, confirm.
2. Their next request through the server fails. The PC leaves their list at the next refresh.

### F5a. Change what someone can open `NEW`

1. Admin, Manage device: tap the person's row. "Change folders" opens with their folders
   ticked.
2. Tick or untick, then "Save". Their row says the new "Can open" list.
3. On the guest's device screen, a removed folder goes away at the next refresh.

### F6. Reach files from outside the house `PLANNED #103`

1. Not on the same Wi-Fi, signed in: Home shows the PC as "Over the internet".
2. Tap the card, then a shared folder. The File browser opens, marked "Remote".
3. If the PC is off: the card says "Can't reach right now" and its folder rows are disabled.

### F7. Signed out somewhere else `BUILT`, `NEW` without a message

1. The password was reset on the website, which ends every sign-in.
2. The app's next renewal is refused, and the app is signed out. The Account screen shows
   "Not signed in" and "Sign in". There is no dot and no "session ended" message. Devices on
   the Wi-Fi keep working.

### F8. Take a PC off the account `BUILT`

1. PC, settings page: "Sign out".
2. The PC keeps sharing on its own network. It no longer shows "Over the internet" for
   anyone, and leaves the list of people who are not on its network.

### F9. A PC that is not what it claims `BUILT`

1. Something on the network advertises a known PC's identity but cannot prove it.
2. Home: that card turns to the error colour with a warning icon and the reason. It cannot be
   opened.

### F10. Update the app `NEW`

1. App, Account, About: "Check for updates". A dialog says "Version {x} is out". "Download".
2. Browser: `/download` opens. Download the new APK or desktop app.
3. Install over the old one. Sign-in and known PCs are kept.

### F11. Delete the account `NEW`

1. App, About: "Delete my account". The browser opens `/account/delete`.
2. Website: sign in if needed, type the password, "Delete my account".
3. App: at the next renewal the app is signed out, as in F7.

---

## 9. Not supported: do not design

- Password reset or email change forms inside the app (sign up is in the app, 5.3.1)
- Sharing a folder, or adding a PC to an account, from the app
- Changing what a PC shares from anywhere except its settings page
- Thumbnails, image or video preview, a media player, a text viewer
- Unzip, or pasting into another shared folder
- File share links, public file links, file links that work without the app
- Upload or download progress in percent, speed or time left (a plain bar only; "3 of 12"
  only with #165)
- Pause, resume, or a transfer queue
- Sorting, filtering, grid view
- Favourites, recent files, offline copies, sync, backup
- Storage used, disk space, quotas
- Device details: OS, model, battery, last seen, IP change history
- Profile pictures, display names, a profile screen
- Roles beyond one admin per PC: no second admin, no "make admin", no role picker
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
| client and agent | Keep working across versions; newer features switch on when both sides have them | #184 |
| agent | The settings page opens when the install finishes | #185 |
| website | The approve page links to sign up and password reset, and comes back | #186 |
| client | An invitation message that works for someone new | #187 |
| cloud and client | A guest leaves a PC ("Leave this PC" on the card) | #188 |
| cloud, client and agent | An admin removes a PC they no longer have ("Remove this PC") | #189 |
| agent | The settings page shows its version and says when a newer one is out | #190 |

#142, #159 and #160 (the macOS menu bar item and the Windows tray icon) are P1.

Changed later on 2026-09-28 while designing in Figma. The Figma design is the reference for
layout; this file is the reference for behaviour and copy.

- Home groups PCs by who owns them: "Your devices", "Shared with you", "Nearby". No role line,
  no address, no folder count on a card (#174).
- One admin per PC, the account signed in on it. Everyone else is a guest. Invitation codes
  only make guests (#191).
- The card's three-dot menu holds "Manage access" and "Remove this PC" for the admin, "Leave
  this PC" for a guest. Both ask first (#188, #189).
- "Join with a code" is a floating button, signed in only (#174).
- First contact shows a "Compare codes" bottom sheet (#176).
- No "needs an update" card. The old protocol stays supported, and a newer feature switches
  on only when both sides support it (#184, rewritten).
- Onboarding on the first launch (5.0), with the local network ask as its third step (#192).

Changed on 2026-09-29, after a Material 3 review against the TachiyomiSY app.

- Invites are links. A link can have no end date and a note, and the admin sees the unused
  ones and can cancel them. Joining happens on the web page at `/join` in any browser, and the
  app has no join screen or button (#187, #193, #194).
- Choosing one of a few fixed options uses a segmented button (the link's end date).
- Dropdown menus are text only, with no icons.
- Text fields inside a dialog are outlined.
- About rows have no leading icons. Account rows have icons in the primary colour.

Redesigned later on 2026-09-29 on the Figma page "Akdes New", after TachiyomiSY's onboarding
was added as a guide. Each row there has a note that gives the reason for every part.

- Onboarding uses one screen for every step: icon, heading and line on top, the step in one
  rounded box, and one filled button at the bottom. Three steps: set up your device, find
  devices on this Wi-Fi (Android 17 and later), account. No step dots and no Skip.
- The Wi-Fi ask is a list row with an outlined "Allow" button. A check replaces it once
  Android says yes.
- Home cards hold their folders as plain list rows. No card sits inside another card.
- Home has one notice card part for update, server down, session ended and sign in. Urgent
  notices go at the top; the sign-in offer goes at the end.
- Invite links have their own screen. Manage access opens it from an "Invite links" row at the
  top, above the people. The screen has a "Create" floating button that makes a link with no
  dialog. The app always makes 7 day links for now, with no note.
- Unused link rows show the link on one line, cut in the middle, and "Expires at {date}".
- A made link shows in an "Invite link" dialog, with "Copy link" on the left and "Remove" on the
  right. The admin can open it again by tapping the link's row, so the link is no longer shown
  only once and there is no Share button.
- Each unused link row has two icon buttons, copy and trash, in place of the "Cancel" text
  button.
- Every text field is outlined, and every main button is the standard filled button.
- The join page opens with an invite card, and its buttons sit under the text.

Changed on 2026-09-29 from comments on the Figma page "Akdes New".

- Home cards hold only the icon, name, reach label and three dots. Tapping a card opens a
  device screen with its folders, so Home stays short with many devices and many folders
  (#174).
- The three dots open a bottom sheet: the same card without the dots, then "Show code",
  "Manage access" and "Remove" for an admin, "Show code" and "Remove" for a guest (#174, #176,
  #188, #189).
- The card has no first-contact line. The fingerprint is behind "Show code" (#176).
- The guest's action is "Remove", with the dialog "Remove office-pc?" (#188).
- Home has no update, sign-in or session-ended notices. The only notice left is "server did
  not answer".
- About starts with "Check for updates". A spinner sits at the end of the row while it checks,
  and a new version shows in a dialog with "Later" and "Download" (#177, #178).
- A sign-in never runs out for now. The app renews it with a refresh token that has no end
  date, so there is no "checking" or "session ended" state (#195).
- An account is made in the app: "Create an account" is an outlined button under "Sign in".
  The account is stored at once, not active, and a 6 digit code is emailed. Typing the code
  activates it and signs the app in. The code can be sent again, at most once a minute
  (#196).
- Onboarding is one screen with two boxes: nearby devices (Android 17 and later) and
  "Account (optional)". "Get started" always works. After two refusals the nearby devices box
  offers "Open settings" and "How to allow permission". The device setup steps move to the Help page,
  opened from "How to set up your device" on the empty Home. There is no signed-in screen (#192).
- The join page is centred top to bottom, shows nothing about the invite until the person
  signs in, and says "Accept invite" (#194).
- The empty Home has one text button, "How to set up your device" with the help icon, in place of "Get ProductName for your device" and the "?" in the top bar. The Help steps start with getting the app, so one button covers both.
- Home has no banners. Nearby devices not allowed shows as the onboarding box inside the Nearby section. Server did not answer shows as "Can't reach right now" on the account's cards, with pull down to try again. Both can be true at once without one hiding the other.
- Home loading says "Looking for your devices…", since it asks the account and the Wi-Fi at once. An empty Home has two versions: nearby devices not allowed shows only the nearby devices box; allowed with nothing found shows "No devices found yet" with "Open ProductName on your device. It shows up here when it's on this Wi-Fi or signed in to your account."
- A card under "Shared with you" says "Shared by" and the owner's email under the reach label.
- An invite link gives only the folders the admin ticks. "Create" opens "Choose folders", a dialog with a scrolling list of the device's folders and "Create link", greyed out until one is ticked. The link dialog says "Can open: files, photos". The server must store the folders on the link and limit the guest to them.
- The Account header shows the plan, "Premium" or "Free", as a small tonal label under the email.
- The "About ProductName" row has no second line.
- Manage access shows what each guest can open, "Can open: files, photos", and a tap on a guest opens "Change folders" with their folders ticked. "Save" is greyed out with nothing ticked, because "Remove" is the way to take all access away. A folder the device starts sharing later is not given to guests. The server needs a way to change a guest's folders, and the audit log records it (#197, #198).
- The file browser gets file actions, from the Material Files app. Every row has three dots that open a bottom sheet: Open with, Save to Downloads, Share, Cut, Copy, Rename, Compress, Properties and Delete. A tap on a file opens it. Cut and Copy end in a "Paste here" bar at the bottom. The floating button is a "+" that opens "Send a file" and "New folder". The refresh icon goes; pull down refreshes. Selection actions move to a bottom bar, as in TachiyomiSY. Rename, move, copy and new folder are already supported by the file server on the device; compress is not (#199, #200).
