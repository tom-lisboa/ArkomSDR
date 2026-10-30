<script>
  let { name = 'book', size = 18 } = $props();
  const paths = {
    book: ['M4 4h6a3 3 0 0 1 3 3v14a4 4 0 0 0-4-2H4z', 'M20 4h-4a3 3 0 0 0-3 3v14a4 4 0 0 1 4-2h3z'],
    search: ['M21 21l-4.5-4.5', 'M18 10.5a7.5 7.5 0 1 1-15 0 7.5 7.5 0 0 1 15 0'],
    arrow: ['M5 12h14', 'm13 6 6 6-6 6'],
    external: ['M14 3h7v7', 'm10 14 11-11', 'M21 14v6a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h6'],
    copy: ['M9 9h12v12H9z', 'M15 3H3v12h3'],
    check: ['m5 12 4 4L20 5'],
    download: ['M12 3v12', 'm7 10 5 5 5-5', 'M4 17v4h16v-4'],
    chevron: ['m9 5 7 7-7 7'],
    close: ['m6 6 12 12', 'M18 6 6 18'],
    menu: ['M4 6h16', 'M4 12h16', 'M4 18h16'],
    code: ['m8 6-6 6 6 6', 'm16 6 6 6-6 6', 'm14 3-4 18'],
    rocket: ['M14 5c2-2 6-2 6-2s0 4-2 6l-7 7-5-5z', 'm7 10-4 1v6l4-1', 'm11 16-1 5h6l1-4', 'm3 21 3-3', 'M15 7h.01'],
    lock: ['M5 10h14v11H5z', 'M8 10V6a4 4 0 0 1 8 0v4', 'M12 15v2'],
    layers: ['m12 2 10 6-10 6L2 8z', 'm2 12 10 6 10-6', 'm2 16 10 6 10-6'],
    hand: ['M8 12V5a2 2 0 0 1 4 0v7', 'M12 10V3a2 2 0 0 1 4 0v10', 'M16 9a2 2 0 0 1 4 0v7a6 6 0 0 1-6 6h-2c-2 0-3-1-4-2L3 14a2 2 0 0 1 3-2l2 2'],
    calendar: ['M3 5h18v16H3z', 'M7 2v6', 'M17 2v6', 'M3 11h18', 'm8 16 3 3 5-5'],
    thermometer: ['M9 14V5a3 3 0 0 1 6 0v9a5 5 0 1 1-6 0', 'M12 9v9'],
    chart: ['M4 3v18h17', 'm7 14 4-4 4 3 6-8'],
    list: ['M9 6h12', 'M9 12h12', 'M9 18h12', 'M3 6h1', 'M3 12h1', 'M3 18h1'],
    shield: ['m12 2 9 4v6c0 6-9 10-9 10S3 18 3 12V6z', 'm8 12 3 3 5-6'],
    network: ['M9 3h6v6H9z', 'M2 16h6v6H2z', 'M16 16h6v6h-6z', 'M12 9v4', 'M5 16v-3h14v3'],
    home: ['m3 10 9-8 9 8', 'M5 9v13h14V9', 'M9 22v-8h6v8'],
    users: ['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M13 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0', 'M22 21v-2a4 4 0 0 0-3-3.9', 'M16 3.1a4 4 0 0 1 0 7.8'],
    info: ['M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0', 'M12 11v6', 'M12 7h.01'],
  };
</script>
<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  {#each (paths[name] || paths.book) as d}<path {d} />{/each}
</svg>
