import http from 'k6/http';
import { Trend } from 'k6/metrics';
import { check } from 'k6';

const statusTrend = new Trend('status_codes');

export const options = {
    stages: [
        { duration: '10s', target: 100 },
        { duration: '20s', target: 100 },
        { duration: '10s', target: 0 },
    ],
};

const BASE_URL = __ENV.ORCH_BASE_URL || 'http://localhost:8080';
const PROCESS_URL = `${BASE_URL}/api/v1/documents/process`;

// k6 resuelve open() relativo a la ubicación de este script (tests/), no al CWD.
const pdfFiles = [
    {
        name: '2020-Scrum-Guide-Spanish-Latin-South-American.pdf',
        data: open('./stress/pdfs/2020-Scrum-Guide-Spanish-Latin-South-American.pdf', 'b'),
    },
    {
        name: 'Essential-Kanban-Condensed-Spanish.pdf',
        data: open('./stress/pdfs/Essential-Kanban-Condensed-Spanish.pdf', 'b'),
    },
    {
        name: 'Filosofia Lean.pdf',
        data: open('./stress/pdfs/Filosofia Lean.pdf', 'b'),
    },
    {
        name: 'scrum_manager_historias_usuario.pdf',
        data: open('./stress/pdfs/scrum_manager_historias_usuario.pdf', 'b'),
    },
];

export default function () {
    // Selección aleatoria de un PDF de la lista
    const randomPdf = pdfFiles[Math.floor(Math.random() * pdfFiles.length)];

    const res = http.post(
        PROCESS_URL,
        { file: http.file(randomPdf.data, randomPdf.name, 'application/pdf') },
    );

    statusTrend.add(res.status);

    check(res, {
        'status 200': (r) => r.status === 200,
    });
}
