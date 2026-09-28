# Prompt for the Figma design agent

Copy everything below the line into the agent, and attach `ui-spec.md` with it.

---

You are designing the UI for **anywhere-file** in Figma. The attached `ui-spec.md` is the only
source of truth. It was written from the product's real code and issue tracker. Your job is to
make that system look good and feel clear. Your job is not to add features.

## Hard rules

1. **Design only what the spec lists.** Every screen, field, button, state and piece of data
   in your frames must trace to a section of `ui-spec.md`. If you cannot point to the section,
   remove it.
2. **Never invent data.** Use only the fields in section 4 of the spec. No avatars, no display
   names, no device OS, no storage bars, no "last seen", no thumbnails, no file previews, no
   percentages or transfer speeds. Section 9 lists what does not exist. Read it before you
   start and check against it before you finish.
3. **Keep the fixed copy.** Text shown in quotes in the spec is what the app says today. Use
   it as written. You may fix layout and hierarchy around it. If you think the words should
   change, keep the original in the frame and put your suggestion in a note next to it.
4. **Respect where things happen.**
   - Sharing a folder and adding a PC to an account happen only on the **PC settings page**,
     never in the client app.
   - Sign up and password reset happen only on the **website**. The app opens the browser.
   - Management (people, remove, invite) is for **admins only**. A guest sees none of it.
5. **Tag every frame.** Put one of these tags in the frame name:
   - `BUILT` for what exists now
   - `PLANNED #N` for what an open issue defines (use the issue number from the spec)
   - `NEW` for what the owner agreed and no issue covers yet (spec section 12)
   Do not design anything the spec tags `SERVER ONLY` unless asked.
6. **When the spec is silent, do not guess.** Add a sticky note on the canvas titled
   "Question" with what is missing, and move on. A visible question is better than an
   invented answer.
7. **Name and words.** The product is called **ProductName** at **productname.example** in
   every frame. Never show networking words to the person: no "LAN", "relay", "tunnel",
   "direct", "online". A device says how the app reaches it with exactly one of: "On your
   Wi-Fi", "Over the internet", "Can't reach right now", "Checking…".
8. **Every flow must be possible.** A prototype link may only go where the spec's flows
   (section 8) or screen descriptions say that action goes. No dead-end buttons, and no button
   that leads to a screen the spec does not have.

## What to deliver

### Page 1: Foundations

- Material 3 based. Light and dark colour schemes built from colour roles (primary,
  primary container, surface, surface container levels, error, error container, outline).
- Note that Android 12+ swaps in wallpaper colours, so meaning must come from roles, never
  from one brand hue.
- Type scale, spacing, corner radii, and the icon set (Material Symbols).
- Components used by the screens: device card (4 states), folder tile, account card
  (4 states), remote device card (admin and guest), code card, list row for folder and file,
  file type icon tile (`PLANNED #163`), breadcrumb (`PLANNED #163`), selection bar
  (`PLANNED #165`), snackbar, dialog, filter chips, top app bar, extended floating button,
  empty and error states.

### Page 2: Client app, phone (412 × 915, and check at 360 × 800)

One frame per screen per state listed in spec section 5:

- 5.1 Local network permission: first ask; after refusal
- 5.2 Home: one device list, no sections. Show a list that mixes every reach label: "On your
  Wi-Fi", "Over the internet", "Can't reach right now", "Checking…", plus a refused card, an
  admin card with "Manage", a guest card, and a device that is not on the account. Also:
  looking; nothing found; signed out (with the sign-in line under the list); server error;
  first contact with fingerprint; update banner
- 5.2.3 Join with a code dialog: idle, busy, error
- 5.8 Account: the four states, trouble line, caveat line, and the Home icon with its dot
- 5.7 About: latest version, and a newer version available
- 5.3 Sign in: empty; filled; busy; each error type
- 5.4 Manage device: people list; remove dialog; last admin error; invite form; code made
  (Android share)
- 5.5 File browser: loading; failed; empty; listing; row menu; transfer running; snackbar;
  upload hidden when not allowed; delete hidden when not allowed
- 5.5 planned: breadcrumbs and type icons (#163); search with both scopes and its states
  (#164); selection mode and "3 of 12" progress and the end-of-run failure message (#165);
  the reach label in the top bar and "Can't reach right now" (#103); Android notification
  permission ask and the transfer notification (#156)

Also dark mode for Home, File browser and Manage device.

### Page 3: Client app, desktop (1024 × 720, and check at 1440 × 900)

The same screens. Desktop has no system back, so the top bar back arrow is always present.
"Share" on a code copies to the clipboard and says so. A wider layout is fine, but it must show
the same content and actions as the phone.

### Page 4: PC settings page (browser, 672 px column)

Spec section 6: shares list (empty and filled); folder walker; name field; account not signed
in; waiting for approval with code; signed in; error line. Plus the planned menu bar (#159)
and tray (#160) menus with their two items.

### Page 5: Website (desktop 1440 wide, and phone 390 wide)

Spec section 7:

- 7.1 Landing page. Every claim must be in the spec's list. Check it against the "must NOT
  make" list: no end-to-end encryption claim, no iOS, no pricing, no sync or backup, no
  "signed" installers, no GitHub link.
- 7.2 Download page with two groups, "Share this PC" and "Reach your PCs", the visitor's
  system first in each.
- 7.3 Help: the index and one topic page as a template.
- 7.4 Privacy and Terms: one layout with placeholder text.
- 7.5 Delete my account: signed out, signed in, wrong password, done.
- 7.6 Account pages: each in its form state and its done state, and the three approve page
  states.
- The shared header and footer.

### Page 6: Flows

Wire the prototype for each flow in spec section 8 (F1 to F11). Each flow is one row of frames,
left to right, across surfaces where the flow crosses them (for example F3 goes settings page,
then website, then settings page, then app). Label each arrow with the action taken.

### Page 7: Traceability

A table: frame name, spec section, tag (`BUILT` or `PLANNED #N`). Every frame appears once.
Then the list of your "Question" notes.

## Before you finish, check

- [ ] No element from spec section 9 appears anywhere.
- [ ] Every field shown is in spec section 4.
- [ ] Every frame is tagged, and in the traceability table.
- [ ] Guest views contain no management.
- [ ] Home has no "On this network" or "Away from home" sections, and no networking words.
- [ ] The website has no pricing, no iOS, no GitHub link, and no end-to-end encryption claim.
- [ ] The client app has no "share a folder", "add this PC", sign up form or reset form.
- [ ] Every button has a destination or a described result from the spec.
- [ ] Long names, emails, paths and codes are shown truncating properly.
- [ ] Light and dark both work, and "ready" and "refused" read by colour role and by icon or
      text, not by colour alone.
