import React from 'react';
import { createRoot } from 'react-dom/client';
import './style.css';

function App() {
    const [message, setMessage] = React.useState('Loading backend…');

    React.useEffect(() => {
        fetch('/api/hello')
            .then((response) => {
                if (!response.ok) throw new Error(`Request failed (${response.status})`);
                return response.json();
            })
            .then((data) => setMessage(data.message))
            .catch((error) => setMessage(error.message));
    }, []);

    return (
        <main>
            <h1>HackTheBorough</h1>
            <p>React frontend connected to the Go backend. 2</p>
            <p className="status">{message}</p>
        </main>
    );
}

createRoot(document.getElementById('root')).render(<App />);
