/** Original campus-day geometry; no game artwork or external assets. */
export default function CampusArt() {
  return <svg className="campus-art" viewBox="0 0 420 180" fill="none" aria-hidden="true">
    <circle cx="285" cy="85" r="76" fill="var(--color-yellow)"/>
    <circle cx="285" cy="85" r="51" stroke="var(--color-ink)" strokeWidth="2"/>
    <circle cx="285" cy="85" r="26" stroke="var(--color-ink)" strokeWidth="2"/>
    <path d="m175 139 132-32 35 17-132 32z" fill="var(--color-accent)"/>
    <path d="m154 51 18-6-4 20 17 11-20 4-5 20-10-17-21 1 13-16-7-20z" fill="var(--color-ink)"/>
    <path d="M36 144h96M52 154h67" stroke="var(--color-ink)" strokeWidth="2"/>
    <path d="m340 16 14 8 15-4-6 14 7 14-16-2-11 11-2-16-14-8 14-5z" fill="var(--color-accent)"/>
  </svg>;
}
