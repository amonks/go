// act.js is the page's half of every act browsertest takes (act.go):
// it finds what a selector names, decides whether a person could act
// on it yet, picks the pixel a pointer would land on, and screens what
// arrives against what was meant. It is installed on every document
// before the page's own scripts, so its listeners on window, in the
// capture phase, hear every event before anything of the page's does;
// an act's evaluation installs it too, for a document it missed.
//
// It is an expression: evaluating it yields the installed library.
(() => {
	const key = Symbol.for("monks.co/browsertest/act");
	if (globalThis[key]) return globalThis[key];

	// describe names an element the way an error should: its tag, id,
	// classes, and the start of its text.
	const describe = (el) => {
		if (!el || el.nodeType !== 1) return String(el);
		let s = el.tagName.toLowerCase();
		if (el.id) s += "#" + el.id;
		for (const c of el.classList) s += "." + c;
		const busy = el.getAttribute("aria-busy");
		if (busy) s += `[aria-busy=${busy}]`;
		// textContent, not innerText: it costs no layout, and a busy
		// <main> can hold a whole transcript.
		const whole = el === document.documentElement || el === document.body;
		const text = whole ? "" : (el.textContent ?? "").slice(0, 200).replace(/\s+/g, " ").trim();
		if (text) s += ` "${text.length > 40 ? text.slice(0, 40) + "…" : text}"`;
		return s;
	};

	// resolve is what sel names now: the first element in document order
	// that it matches, and whose text holds o.text when one is given.
	// An unparseable selector is the test's mistake, not a page that
	// isn't ready, so it throws rather than waiting.
	const resolve = (sel, o) => {
		let all;
		try {
			all = document.querySelectorAll(sel);
		} catch (e) {
			throw new Error(`${sel} is not a selector: ${e.message}`);
		}
		for (const el of all) {
			if (o.text == null) return el;
			const text = (el.innerText ?? el.textContent ?? "").replace(/\s+/g, " ");
			if (text.includes(o.text)) return el;
		}
		return null;
	};

	const named = (sel, o) => (o.text == null ? sel : `${sel} holding "${o.text}"`);

	const textual = new Set(["", "text", "search", "url", "tel", "password", "email", "number"]);
	const editable = (el) =>
		el.isContentEditable ||
		el.tagName === "TEXTAREA" ||
		(el.tagName === "INPUT" && textual.has(el.getAttribute("type")?.toLowerCase() ?? ""));

	// unready says why a person could not yet act on el, or "" when
	// they could. Each reason is one a page passes through on its way
	// to ready: not rendered yet, hidden, disabled, read-only, or
	// inside a region the page is replacing (aria-busy). A file input
	// is commonly hidden behind the label a person clicks, so it alone
	// needn't be seen.
	const unready = (el, o) => {
		if (o.kind !== "files") {
			if (!el.checkVisibility({ checkVisibilityCSS: true, visibilityProperty: true })) return "is not visible";
			const box = el.getBoundingClientRect();
			if (box.width === 0 || box.height === 0) return "has no size";
		}
		if (o.kind !== "read" && o.kind !== "see" && o.kind !== "hover") {
			if (el.matches(":disabled")) return "is disabled";
			const inert = el.closest("[inert]");
			if (inert) return `is inert (inside ${describe(inert)})`;
			const off = el.closest('[aria-disabled="true"]');
			if (off) return off === el ? "is aria-disabled" : `is inside ${describe(off)}, which is aria-disabled`;
		}
		if (o.kind === "type" && !editable(el)) return "is not editable";
		if (o.kind === "type" && (el.readOnly || el.matches("[readonly]"))) return "is read-only";
		if (!o.whileBusy) {
			const busy = el.closest('[aria-busy="true"]');
			if (busy) return busy === el ? "is aria-busy" : `is inside ${describe(busy)}, which is aria-busy`;
		}
		return "";
	};

	// interior is the integer coordinate a pointer lands on for a point
	// p along [lo, hi): the integers i with lo <= i < hi are the pixels
	// the browser hit-tests inside the box, and a fractional coordinate
	// is rounded by Chromium into whichever pixel it likes — the
	// neighbour's, at an edge (CI run 2341). A pixel in from each edge
	// where the box has room, since borders and rounded corners draw
	// there. NaN when no pixel falls inside.
	const interior = (lo, hi, p) => {
		let a = Math.ceil(lo);
		let b = Math.ceil(hi) - 1;
		if (b - a >= 2) {
			a += 1;
			b -= 1;
		}
		if (a > b) return NaN;
		return Math.min(b, Math.max(a, Math.round(p)));
	};

	// inView is whether a point is in the viewport, scrollbars excluded.
	const inView = (x, y) => {
		const root = document.documentElement;
		return x >= 0 && y >= 0 && x < (root.clientWidth || innerWidth) && y < (root.clientHeight || innerHeight);
	};

	// aim picks the pixel at fraction (fx, fy) of el's box, scrolled
	// into view, and says what a pointer there would land on.
	const aim = (el, fx, fy) => {
		const at = () => {
			const r = [...el.getClientRects()].find((r) => r.width > 0 && r.height > 0) ?? el.getBoundingClientRect();
			return { x: interior(r.left, r.right, r.left + fx * r.width), y: interior(r.top, r.bottom, r.top + fy * r.height) };
		};
		let p = at();
		if (!inView(p.x, p.y)) {
			el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
			p = at();
		}
		if (!inView(p.x, p.y) && !Number.isNaN(p.x) && !Number.isNaN(p.y)) {
			// A box larger than the viewport: bring the point itself in.
			const root = document.documentElement;
			scrollBy({ left: p.x - (root.clientWidth || innerWidth) / 2, top: p.y - (root.clientHeight || innerHeight) / 2, behavior: "instant" });
			p = at();
		}
		if (Number.isNaN(p.x) || Number.isNaN(p.y)) return { problem: "has no whole pixel to land on" };
		if (!inView(p.x, p.y)) return { problem: `cannot be scrolled to (${p.x}, ${p.y})` };
		let hit = document.elementFromPoint(p.x, p.y);
		if (hit !== el && !el.contains(hit)) {
			// In the viewport but clipped by something that scrolls on
			// its own — a menu, a sidebar: a person scrolls that to it.
			el.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "instant" });
			p = at();
			hit = document.elementFromPoint(p.x, p.y);
		}
		if (hit !== el && !el.contains(hit)) return { problem: `is covered at (${p.x}, ${p.y}) by ${describe(hit)}` };
		// A frame's document is another window, whose events the screen
		// here never hears: such a press cannot be screened.
		return { x: p.x, y: p.y, framed: /^(IFRAME|FRAME|OBJECT|EMBED)$/.test(hit.tagName) };
	};

	// The act in flight, if any: what it meant, and what the screen
	// decided about what arrived.
	let armed = null;
	let serial = 0;

	// doc tells this document from the next one in the same tab: an act
	// whose verdict finds a different document took the page somewhere.
	const doc = Math.random().toString(36).slice(2);

	// The events that open a gesture, by the act that expects them. The
	// screen judges the first to arrive and holds the rest of the
	// gesture to that verdict.
	const opens = {
		pointer: new Set(["pointerdown", "mousedown", "touchstart"]),
		hover: new Set(["pointermove", "mousemove", "pointerover", "mouseover"]),
		type: new Set(["beforeinput", "keydown"]),
		key: new Set(["keydown"]),
		files: new Set(["input", "change"]),
	};

	// screen hears every event the act's input raises before the page
	// does. An event that lands where the act meant — inside what its
	// selector names at the moment the event arrives, so a node the page
	// swapped for an equal one still counts — goes on to the page. One
	// that lands anywhere else is stopped and its default prevented,
	// with every later event of the same gesture: the page never sees a
	// press, a keystroke, or text meant for something else, and the act
	// tries again.
	//
	// What it meant must also still be outside any region the page is
	// replacing: a refresh that began in the round trip between aiming
	// and the input arriving would throw away whatever the input did.
	// Refused, the act waits for the region to settle and aims again.
	const screen = (ev) => {
		const a = armed;
		if (!a || !ev.isTrusted) return;
		const target = ev.composedPath()[0] ?? ev.target;
		if (a.verdict === null && opens[a.kind].has(ev.type)) {
			const meant = resolve(a.sel, a.o);
			const busy = meant && !a.o.whileBusy ? meant.closest('[aria-busy="true"]') : null;
			if (!meant || (meant !== target && !meant.contains(target))) a.verdict = `landed on ${describe(target)}`;
			else if (busy) a.verdict = `landed in ${describe(busy)} while it was aria-busy`;
			else a.verdict = "";
			a.pressed = target;
		}
		if (a.verdict) {
			ev.stopImmediatePropagation();
			if (ev.cancelable) ev.preventDefault();
		} else if (a.verdict === "" && ev.type === "click") {
			a.clicked = true;
		}
	};
	const screened = [
		"pointerdown", "mousedown", "touchstart", "pointerup", "mouseup", "touchend", "touchcancel", "pointercancel",
		"click", "auxclick", "dblclick", "contextmenu",
		"pointermove", "mousemove", "pointerover", "mouseover", "pointerenter", "mouseenter",
		"beforeinput", "input", "keydown", "keypress", "keyup",
	];
	for (const type of screened) addEventListener(type, screen, { capture: true, passive: false });

	// pressed notes, once a press has run through the page's own
	// handlers (on window, in the bubble phase: after theirs), whether
	// what was pressed is still in the document. A control that acts on
	// its press — a suggestion chip on mousedown — may replace itself
	// then, and its press was its act; a node still there now and gone
	// by the release was replaced by something else in between, which
	// is how a refresh loses a person's click.
	const pressed = () => {
		const a = armed;
		if (a && a.verdict === "" && a.pressed) a.survivedPress = a.pressed.isConnected;
	};
	for (const type of ["pointerdown", "mousedown"]) addEventListener(type, pressed);

	const lib = {
		describe,
		resolve,

		// ready waits, in the page, until sel names something a person
		// could act on as o.kind asks, or until ms run out. When ready it
		// prepares the act in the same task — aims, focuses, selects —
		// and arms the screen, so nothing of the page's runs between the
		// check and the act's start.
		ready(sel, o, ms) {
			const deadline = performance.now() + ms;
			return new Promise((done, fail) => {
				const look = () => {
					let why;
					try {
						why = lib.attempt(sel, o);
					} catch (e) {
						fail(e);
						return;
					}
					if (typeof why !== "string") {
						done(why);
						return;
					}
					if (performance.now() >= deadline) {
						done({ problem: `${named(sel, o)} ${why}` });
						return;
					}
					setTimeout(look, 16);
				};
				look();
			});
		},

		// attempt is one look: a string saying why not, or the act armed.
		attempt(sel, o) {
			const el = resolve(sel, o);
			if (!el) return "matches nothing";
			const why = unready(el, o);
			if (why) return why;
			armed = null;
			let out = {};
			switch (o.kind) {
				case "read":
					return { text: el.innerText, value: el.value ?? null };
				case "see":
					return { seen: true };
				case "pointer":
				case "hover": {
					const p = aim(el, o.fx, o.fy);
					if (p.problem) return p.problem;
					out = { x: p.x, y: p.y, framed: p.framed };
					break;
				}
				case "type":
				case "key":
					el.focus({ preventScroll: false });
					if (!(document.activeElement === el || el.contains(document.activeElement))) return "does not take focus";
					if (o.kind === "type" && (el.tagName === "INPUT" || el.tagName === "TEXTAREA")) {
						if (o.replace) el.select();
						else if (el.selectionStart !== null) el.setSelectionRange(el.value.length, el.value.length);
						// A number or email field has no caret a script can
						// place, and focus puts it before the value: typing
						// after a value there is not something Type can say.
						else if (el.value !== "") throw new Error(`${describe(el)} has no caret to place after its value; Fill it instead`);
					} else if (o.kind === "type" && o.replace) {
						getSelection().selectAllChildren(el);
					}
					break;
				case "value": {
					lib.setValue(el, o.value);
					return { done: true };
				}
				case "files":
					out = { element: el };
					break;
				default:
					throw new Error(`no act of kind ${o.kind}`);
			}
			serial += 1;
			armed = { sel, o, kind: o.kind, token: serial, verdict: null, clicked: false, pressed: null, survivedPress: false };
			return { ...out, token: serial, doc };
		},

		// armedAct is the act armed now, for an act whose ready handed
		// back an element rather than its token (files).
		armedAct() {
			return armed ? { token: armed.token, doc } : null;
		},

		// verdict ends the act token armed in document from and says what
		// became of it, as an outcome and why:
		//   delivered  what it meant received it;
		//   elsewhere  this is another document: the act's input took
		//              the page somewhere, a link followed, a form sent;
		//   refused    it landed elsewhere, or in a busy region, and was
		//              stopped: aim again;
		//   nothing    no event of the act reached the page: aim again;
		//   lost       a press reached what it meant and survived its
		//              own handlers, and the page replaced it before the
		//              release, so no click came of it: the act happened
		//              only in part. (A press whose own handlers replaced
		//              what was pressed was the control's act, and is
		//              delivered.)
		// A click comes of a press only once the release arrives, and a
		// tap's only from the gesture the browser recognizes after the
		// touch ends, so a pointer act's verdict waits for it, boundedly.
		// Files don't wait: their events may trail them, and SetFiles
		// reads no event as the files having reached the input it holds.
		async verdict(token, from) {
			if (from !== doc) return { outcome: "elsewhere" };
			const a = armed;
			if (!a || a.token !== token) return { outcome: "refused", why: "the act was superseded" };
			const pointer = a.kind === "pointer";
			const until = performance.now() + 2000;
			const waiting = () => pointer && a.verdict === "" && !a.clicked && a.pressed?.isConnected;
			while (waiting() && performance.now() < until) await new Promise((r) => setTimeout(r, 16));
			if (armed === a) armed = null;
			if (a.verdict === null) return { outcome: "nothing", why: "nothing arrived" };
			if (a.verdict !== "") return { outcome: "refused", why: a.verdict };
			if (pointer && !a.clicked && a.survivedPress && !a.pressed.isConnected) {
				return { outcome: "lost", why: `pressed ${describe(a.pressed)}, which the page replaced before the release, so no click arrived` };
			}
			return { outcome: "delivered" };
		},

		// settle waits up to ms for the page to settle, and says what
		// still keeps it busy if it does not.
		settle(ms) {
			const deadline = performance.now() + ms;
			const settled = () => document.readyState === "complete" && !document.querySelector('[aria-busy="true"]');
			return new Promise((done) => {
				const look = () => {
					if (settled()) done([]);
					else if (performance.now() >= deadline) done(lib.busy());
					else setTimeout(look, 16);
				};
				look();
			});
		},

		// setValue sets a control's value as a person's choice would:
		// the value, then input and change, in one task.
		setValue(el, value) {
			if (el.tagName === "SELECT" && el.multiple && Array.isArray(value)) {
				for (const opt of el.options) opt.selected = value.includes(opt.value);
			} else if (el.type === "checkbox" || el.type === "radio") {
				el.checked = Boolean(value);
			} else {
				el.value = value;
				if (el.value !== String(value)) throw new Error(`${describe(el)} would not take the value ${JSON.stringify(value)}`);
			}
			el.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
			el.dispatchEvent(new Event("change", { bubbles: true }));
		},

		// owns says whether el is still what sel names: SetFiles' check,
		// when no event of its files arrived to judge (a page with its
		// scripting off hears none), that they reached the live input.
		owns(el, sel, o) {
			return resolve(sel, o) === el;
		},

		// busy lists what the document marks aria-busy, for the wait for
		// a page to settle.
		busy() {
			if (document.readyState !== "complete") return [`the document is ${document.readyState}`];
			return [...document.querySelectorAll('[aria-busy="true"]')].map(describe);
		},
	};
	Object.defineProperty(globalThis, key, { value: lib });
	return lib;
})()
