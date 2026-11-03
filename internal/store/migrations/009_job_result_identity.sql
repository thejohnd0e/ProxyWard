ALTER TABLE availability_results ADD COLUMN job_id INTEGER REFERENCES jobs(id) ON DELETE RESTRICT;
ALTER TABLE speed_results ADD COLUMN job_id INTEGER REFERENCES jobs(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX availability_one_result_per_job ON availability_results (job_id) WHERE job_id IS NOT NULL;
CREATE UNIQUE INDEX speed_one_result_per_job ON speed_results (job_id) WHERE job_id IS NOT NULL;
