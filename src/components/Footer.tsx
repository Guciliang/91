export function Footer() {
  return (
    <footer className="footer">
      <div className="container footer__inner">
        <a
          className="footer__copy"
          href="https://github.com/nianzhibai/91"
          target="_blank"
          rel="noopener noreferrer"
        >
          © {new Date().getFullYear()} 91
        </a>
      </div>
    </footer>
  );
}
