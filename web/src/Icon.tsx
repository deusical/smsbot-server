const paths: Record<string, string> = {
  archive: "M3 4h18v5H3V4Zm2 5v12h14V9M9 13h6",
  home: "M3 10.5 12 3l9 7.5M5 9v12h14V9M9 21v-8h6v8",
  conversations:
    "M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-1 1 1-5.5A8.5 8.5 0 1 1 21 11.5Z",
  commands: "M12 4v10m0 4v2",
  activity: "M3 12h4l3-8 4 16 3-8h4",
  settings:
    "M4 6h10m4 0h2M4 12h4m4 0h8M4 18h12m4 0h0M16 4v4M10 10v4M18 16v4",
  arrow: "M5 12h14m-6-6 6 6-6 6",
  back: "M19 12H5m6-6-6 6 6 6",
  shield: "m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z",
  phone:
    "M8 2h8a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2Zm3 16h2",
  spark: "m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5L12 3Z",
  search: "M10.5 18a7.5 7.5 0 1 1 0-15 7.5 7.5 0 0 1 0 15Zm5.5-2 5 5",
  moon: "M21 13a9 9 0 1 1-10-10 7 7 0 0 0 10 10Z",
  sun: "M12 3V1m0 22v-2M3 12H1m22 0h-2M4 4 2.5 2.5M21.5 21.5 20 20M4 20l-1.5 1.5M21.5 2.5 20 4M17 12a5 5 0 1 1-10 0 5 5 0 0 1 10 0Z",
  check: "m5 12 4 4L19 6",
  x: "M6 6l12 12M18 6 6 18",
  pause: "M8 4v16M16 4v16",
  play: "m7 3 14 9-14 9V3Z",
  plus: "M12 5v14M5 12h14",
  clock: "M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z",
  logout: "M9 3H4v18h5m4-14 5 5-5 5m-4-5h12",
  copy: "M8 8h12v12H8V8ZM4 16H2V2h14v2",
  warning: "m12 3 10 18H2L12 3Zm0 6v5m0 3v1",
  send: "M4 12 20 4l-4 16-4-7-8-1Zm8 1 8-9",
  star: "m12 3 2.8 5.7 6.2.9-4.5 4.4 1 6.2L12 17.3 6.5 20.2l1-6.2L3 9.6l6.2-.9L12 3Z",
  chevron: "m6 9 6 6 6-6",
  trash: "M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13",
  key: "M14 10a4 4 0 1 0-3.5 4L12 15.5V18h2.5v2.5H17V18l-3-3.1A4 4 0 0 0 14 10Zm-5-1h.01",
  cpu: "M7 7h10v10H7V7Zm3-4v4m4-4v4m-4 10v4m4-4v4M3 10h4m-4 4h4m10-4h4m-4 4h4",
  gauge: "M4 18a9 9 0 1 1 16 0M12 13l4-4",
  inbox: "M3 13h5l1.5 3h5L16 13h5M5 5h14l2 8v6H3v-6l2-8Z",
  attach: "m8 12 6-6a3 3 0 0 1 4 4l-8 8a5 5 0 0 1-7-7l9-9m-6 12 8-8",
  download: "M12 3v12m-5-5 5 5 5-5M4 20h16",
  refresh: "M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7",
  more: "M5 12h.01M12 12h.01M19 12h.01",
  user: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Zm-8 9a8 8 0 0 1 16 0",
  history: "M3 12a9 9 0 1 0 3-6.7L3 8m0-5v5h5m4-1v5l3 2",
};
export function Icon({
  name,
  size = 20,
  filled = false,
}: {
  name: string;
  size?: number;
  filled?: boolean;
}) {
  if (name === "commands")
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        aria-hidden="true"
        fill="currentColor"
      >
        <rect x="10" y="3" width="4" height="12" rx="2" />
        <circle cx="12" cy="19.5" r="2.2" />
      </svg>
    );
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill={filled ? "currentColor" : "none"}
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name] ?? paths.spark} />
    </svg>
  );
}
